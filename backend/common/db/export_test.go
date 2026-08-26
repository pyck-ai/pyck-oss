package db

import (
	"database/sql"

	"entgo.io/ent/dialect"
	_ "github.com/mattn/go-sqlite3"
)

// BuildPoolUri is exported for testing.
var BuildPoolUri = buildPoolUri

// BuildHealthUri is exported for testing.
var BuildHealthUri = buildHealthUri

// DriverOpts exposes the (unexported) option-application path so tests can
// verify functional options compose correctly without opening a real pool.
type DriverOpts = driverOpts

// NewDriverOpts returns the production defaults that NewPostgresMultiDriver
// applies before running caller-supplied Options.
func NewDriverOpts() DriverOpts {
	return driverOpts{
		writerIsolation: "serializable",
		readerIsolation: "read committed",
	}
}

// WriterIsolation reads the (unexported) writerIsolation field for testing.
func (o DriverOpts) WriterIsolation() string { return o.writerIsolation }

// ReaderIsolation reads the (unexported) readerIsolation field for testing.
func (o DriverOpts) ReaderIsolation() string { return o.readerIsolation }

// ApplyOption invokes an Option against o for testing.
func (o *DriverOpts) ApplyOption(opt Option) { opt(o) }

// MultiDriver is exported for testing the routing behaviour of
// pgMultiDriver without standing up real Postgres pools.
type MultiDriver = pgMultiDriver

// NewMultiDriverWithDrivers wires a pgMultiDriver from caller-supplied
// reader / writer dialect.Drivers. Tests use this to inject fakes that
// record which pool a call landed on. The health pool is a throwaway
// in-memory database so the constructor upholds the production invariant
// that healthDb is never nil (Close relies on it).
func NewMultiDriverWithDrivers(reader, writer dialect.Driver) *MultiDriver {
	healthDb, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		panic(err)
	}
	return &pgMultiDriver{reader: reader, writer: writer, healthDb: healthDb}
}
