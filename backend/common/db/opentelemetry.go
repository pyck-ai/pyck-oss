package db

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strings"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
)

const (
	currentSchemaPattern           = `(?i)SELECT\s+current_schema\b`
	createSchemaPattern            = `(?i)CREATE\s+SCHEMA\s+IF\s+NOT\s+EXISTS\b`
	currentDbPattern               = `(?i)SELECT\s+CURRENT_DATABASE\b`
	advisoryLockPattern            = `(?i)SELECT\s+pg_advisory_lock\b`
	advisoryUnLockPattern          = `(?i)SELECT\s+pg_advisory_unlock\b`
	informationSchemaTablesPattern = `(?i)SELECT\s+(.+)\s+FROM\s+information_schema\.tables\b`
	schemaMigrationPattern         = `(?i)SELECT\s+(.+)\s+FROM\s+(.+).schema_migrations\b`
)

var ignoreQueryRegex = regexp.MustCompile(strings.Join([]string{
	currentSchemaPattern,
	createSchemaPattern,
	currentDbPattern,
	advisoryLockPattern,
	advisoryUnLockPattern,
	informationSchemaTablesPattern,
	schemaMigrationPattern,
}, "|"))

func otplAttributes(schemaName string) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.DBSystemPostgreSQL,
		semconv.DBNameKey.String(schemaName),
		attribute.Key("agent.version").String(otelsql.Version()),
	}
}

func otplSpanOptions() otelsql.SpanOptions {
	return otelsql.SpanOptions{
		Ping:                 false,
		RowsNext:             false,
		DisableErrSkip:       false,
		DisableQuery:         false,
		RecordError:          otplRecordError,
		OmitConnResetSession: true,
		OmitConnPrepare:      true,
		OmitConnQuery:        true,
		OmitRows:             true,
		OmitConnectorConnect: true,
		SpanFilter:           otplSpanFilter,
	}
}

// If return value is true, the error will be recorded on the current span. Else it will be ignored.
func otplRecordError(err error) bool {
	return err != nil
}

func otplSpanFilter(ctx context.Context, method otelsql.Method, query string, args []driver.NamedValue) bool {
	return !ignoreQueryRegex.MatchString(query)
}

func otplSpanFormatter(ctx context.Context, method otelsql.Method, query string) string {
	return query
}
