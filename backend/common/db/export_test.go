package db

import (
	"entgo.io/ent/dialect"
)

// BuildPoolUri is exported for testing.
var BuildPoolUri = buildPoolUri

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
// record which pool a call landed on.
func NewMultiDriverWithDrivers(reader, writer dialect.Driver) *MultiDriver {
	return &pgMultiDriver{reader: reader, writer: writer}
}
