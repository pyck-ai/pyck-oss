package db

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/XSAM/otelsql"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/pyck-ai/pyck/backend/common/env/config"
)

const (
	maxOpenConnections    = 50
	maxIdleConnections    = 25
	dbDriver              = dialect.Postgres
	maxConnLifetime       = time.Minute * 30
	maxConnIdleTime       = time.Minute * 10
	healthPoolPingTimeout = time.Second * 5
)

type pgMultiDriver struct {
	reader   dialect.Driver
	writer   dialect.Driver
	writerDb *sql.DB
	healthDb *sql.DB
}

var _ dialect.Driver = (*pgMultiDriver)(nil)

// driverOpts holds the per-call configuration for NewPostgresMultiDriver.
type driverOpts struct {
	writerIsolation string
	readerIsolation string
}

// Option mutates a driverOpts. Use the With* helpers below to construct values.
type Option func(*driverOpts)

// WithWriterIsolation sets the PostgreSQL default_transaction_isolation level
// for the writer pool. Valid values are the lowercase libpq spellings, e.g.
// "serializable", "repeatable read", "read committed".
func WithWriterIsolation(level string) Option {
	return func(o *driverOpts) {
		o.writerIsolation = level
	}
}

// WithReaderIsolation sets the PostgreSQL default_transaction_isolation level
// for the reader pool.
func WithReaderIsolation(level string) Option {
	return func(o *driverOpts) {
		o.readerIsolation = level
	}
}

func NewPostgresMultiDriver(ctx context.Context, serviceName string, config config.DbConfig, opts ...Option) (*pgMultiDriver, error) {
	cfg := driverOpts{
		writerIsolation: "serializable",
		readerIsolation: "read committed",
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	writerUri, err := buildPoolUri(config.DbMasterUrl, serviceName, cfg.writerIsolation)
	if err != nil {
		return nil, err
	}

	writer, db, err := poolFromUri(writerUri)
	if err != nil {
		return nil, err
	}

	readerURI, err := buildPoolUri(config.DbSlaveUrl, serviceName, cfg.readerIsolation)
	if err != nil {
		return nil, err
	}

	reader, _, err := poolFromUri(readerURI)
	if err != nil {
		return nil, err
	}

	healthDb, err := healthPoolFromUri(ctx, buildHealthUri(writerUri, serviceName))
	if err != nil {
		return nil, err
	}

	return &pgMultiDriver{reader: reader, writer: writer, writerDb: db, healthDb: healthDb}, nil
}

// buildHealthUri derives the health pool's connection string from the
// already-shaped writer URI, adding an application_name so the probe
// connection is identifiable in pg_stat_activity. writerUri has been parsed
// once before (by buildPoolUri), so parsing cannot fail here.
//
// The pool this feeds backs /health/ready, not /health.
func buildHealthUri(writerUri, serviceName string) string {
	parsed, err := url.Parse(writerUri)
	if err != nil {
		return writerUri
	}
	applyQueryArgs(parsed, map[string]string{
		"application_name": serviceName + "-health",
	})
	return parsed.String()
}

// healthPoolFromUri opens the single-connection pool backing the
// /health/ready endpoint. It is kept separate from the reader/writer pools so
// that probe queries never wait behind request traffic for a pool slot:
// readiness must keep answering while the service is busy. The pool is deliberately not
// instrumented with otelsql — probes fire every few seconds and would
// otherwise flood tracing with identical spans. The pool is pre-warmed with
// a ping at construction: /health is a static liveness handler that never
// touches the database, so nothing else warms this pool before the first
// readiness probe.
//
// The connection has no lifetime or idle timeout: it is meant to stay open
// for the whole server lifecycle. A broken connection is still replaced —
// database/sql discards it on error and re-dials on the next probe.
func healthPoolFromUri(ctx context.Context, uri string) (*sql.DB, error) {
	db, err := sql.Open(dbDriver, uri)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pingCtx, cancel := context.WithTimeout(ctx, healthPoolPingTimeout)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return db, nil
}

// buildPoolUri parses rawUrl and applies the per-pool query args (search_path
// and default_transaction_isolation). Extracted from NewPostgresMultiDriver so
// that the URL-shaping logic can be unit tested without opening a real pool.
func buildPoolUri(rawUrl, serviceName, isolation string) (string, error) {
	parsed, err := url.Parse(rawUrl)
	if err != nil {
		return "", err
	}
	applyQueryArgs(parsed, map[string]string{
		"search_path":                   serviceName,
		"default_transaction_isolation": isolation,
	})
	return parsed.String(), nil
}

func applyQueryArgs(uri *url.URL, args map[string]string) {
	q := uri.Query()

	for k, v := range args {
		q.Set(k, v)
	}

	uri.RawQuery = q.Encode()
}

func poolFromUri(uri string) (dialect.Driver, *sql.DB, error) {
	schemaName := dbSchemaFromUri(uri)

	otelsqlOptions := []otelsql.Option{
		otelsql.WithAttributes(otplAttributes(schemaName)...),
		otelsql.WithSpanNameFormatter(otplSpanFormatter),
		otelsql.WithSpanOptions(otplSpanOptions()),
	}

	driverName, err := otelsql.Register(dbDriver)
	if err != nil {
		return nil, nil, err
	}

	db, err := otelsql.Open(driverName, uri, otelsqlOptions...)
	if err != nil {
		return nil, nil, err
	}

	if err = db.Ping(); err != nil {
		return nil, nil, errors.New("failed to ping database")
	}

	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxLifetime(maxConnLifetime)
	db.SetConnMaxIdleTime(maxConnIdleTime)

	_, err = otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(otplAttributes(schemaName)...))
	if err != nil {
		return nil, nil, err
	}

	driver := entsql.OpenDB(dialect.Postgres, db)
	return driver, db, nil
}

func dbSchemaFromUri(uri string) string {
	parsedUri, err := url.Parse(uri)
	if err != nil || !parsedUri.Query().Has("search_path") {
		return ""
	}
	return parsedUri.Query().Get("search_path")
}

func (driver *pgMultiDriver) Query(ctx context.Context, query string, args, v any) error {
	executor := driver.reader
	if ent.QueryFromContext(ctx) == nil {
		executor = driver.writer
	}
	return executor.Query(ctx, query, args, v)
}

func (driver *pgMultiDriver) Exec(ctx context.Context, query string, args, v any) error {
	return driver.writer.Exec(ctx, query, args, v)
}

func (driver *pgMultiDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	return driver.writer.Tx(ctx)
}

func (driver *pgMultiDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	executor := driver.writer
	if opts != nil && opts.ReadOnly {
		executor = driver.reader
	}
	beginTxer, ok := executor.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	})
	if !ok {
		return nil, ErrDriverLacksBeginTx
	}
	return beginTxer.BeginTx(ctx, opts)
}

func (driver *pgMultiDriver) Close() error {
	readerErr := driver.reader.Close()
	writerError := driver.writer.Close()
	healthErr := driver.healthDb.Close()

	if readerErr != nil {
		return readerErr
	}

	if writerError != nil {
		return writerError
	}

	if healthErr != nil {
		return healthErr
	}

	return nil
}

func (driver *pgMultiDriver) Dialect() string {
	return driver.writer.Dialect()
}

func (driver *pgMultiDriver) DB() *sql.DB {
	return driver.writerDb
}

// HealthDB returns the dedicated single-connection pool for the /health/ready
// endpoint. It must only be used for health checks: it holds one connection,
// so any other user would serialize with the probes and defeat its purpose.
func (driver *pgMultiDriver) HealthDB() *sql.DB {
	return driver.healthDb
}
