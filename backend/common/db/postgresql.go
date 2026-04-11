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
	maxIdleConnections = 5
	dbDriver           = dialect.Postgres
	maxConnLifetime    = time.Minute * 5
	maxConnIdleTime    = time.Minute
)

type pgMultiDriver struct {
	reader   dialect.Driver
	writer   dialect.Driver
	writerDb *sql.DB
}

var _ dialect.Driver = (*pgMultiDriver)(nil)

func NewPostgresMultiDriver(serviceName string, config config.DbConfig) (*pgMultiDriver, error) {
	writerUri, err := url.Parse(config.DbMasterUrl)
	if err != nil {
		return nil, err
	}
	applyQueryArgs(writerUri, map[string]string{
		"search_path":                   serviceName,
		"default_transaction_isolation": "serializable",
	})

	writer, db, err := poolFromUri(writerUri.String())
	if err != nil {
		return nil, err
	}

	readerURI, err := url.Parse(config.DbSlaveUrl)
	if err != nil {
		return nil, err
	}
	applyQueryArgs(readerURI, map[string]string{
		"search_path":                   serviceName,
		"default_transaction_isolation": "read committed",
	})

	reader, _, err := poolFromUri(readerURI.String())
	if err != nil {
		return nil, err
	}

	return &pgMultiDriver{reader: reader, writer: writer, writerDb: db}, nil
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

	err = otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(otplAttributes(schemaName)...))
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
	return driver.writer.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	}).BeginTx(ctx, opts)
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
