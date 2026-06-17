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
	maxOpenConnections = 50
	maxIdleConnections = 25
	dbDriver           = dialect.Postgres
	maxConnLifetime    = time.Minute * 30
	maxConnIdleTime    = time.Minute * 10
)

type pgMultiDriver struct {
	reader   dialect.Driver
	writer   dialect.Driver
	writerDb *sql.DB
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

func NewPostgresMultiDriver(serviceName string, config config.DbConfig, opts ...Option) (*pgMultiDriver, error) {
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

	return &pgMultiDriver{reader: reader, writer: writer, writerDb: db}, nil
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

	if readerErr != nil {
		return readerErr
	}

	if writerError != nil {
		return writerError
	}

	return nil
}

func (driver *pgMultiDriver) Dialect() string {
	return driver.writer.Dialect()
}

func (driver *pgMultiDriver) DB() *sql.DB {
	return driver.writerDb
}
