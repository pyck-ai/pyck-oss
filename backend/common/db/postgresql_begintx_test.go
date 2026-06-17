package db_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"entgo.io/ent/dialect"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// fakeDriver is a minimal dialect.Driver that also implements the optional
// BeginTx interface pgMultiDriver type-asserts. It records every call so
// tests can assert which pool (reader vs writer) handled a request.
type fakeDriver struct {
	label       string
	beginTxN    int
	lastOpts    *sql.TxOptions
	beginTxErr  error
	txCalled    int
	queryCalled int
	execCalled  int
}

func (f *fakeDriver) Exec(_ context.Context, _ string, _, _ any) error {
	f.execCalled++
	return nil
}

func (f *fakeDriver) Query(_ context.Context, _ string, _, _ any) error {
	f.queryCalled++
	return nil
}

func (f *fakeDriver) Tx(_ context.Context) (dialect.Tx, error) {
	f.txCalled++
	return nopTx{}, nil
}

func (f *fakeDriver) BeginTx(_ context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	f.beginTxN++
	f.lastOpts = opts
	if f.beginTxErr != nil {
		return nil, f.beginTxErr
	}
	return nopTx{}, nil
}

func (f *fakeDriver) Close() error    { return nil }
func (f *fakeDriver) Dialect() string { return dialect.Postgres }

// nopTx satisfies dialect.Tx for tests that never touch the returned tx.
type nopTx struct{}

func (nopTx) Exec(_ context.Context, _ string, _, _ any) error  { return nil }
func (nopTx) Query(_ context.Context, _ string, _, _ any) error { return nil }
func (nopTx) Commit() error                                     { return nil }
func (nopTx) Rollback() error                                   { return nil }

// TestBeginTx_ReadOnly_RoutesToReader confirms that a read-only TxOptions
// causes pgMultiDriver to dispatch BeginTx to the reader pool. This is the
// load-bearing change for Phase 8: GraphQL queries opt into ReadOnly so the
// reader replica actually pulls weight.
func TestBeginTx_ReadOnly_RoutesToReader(t *testing.T) {
	t.Parallel()

	reader := &fakeDriver{label: "reader"}
	writer := &fakeDriver{label: "writer"}

	driver := db.NewMultiDriverWithDrivers(reader, writer)

	opts := &sql.TxOptions{ReadOnly: true}
	if _, err := driver.BeginTx(context.Background(), opts); err != nil {
		t.Fatalf("BeginTx returned error: %v", err)
	}

	if reader.beginTxN != 1 {
		t.Errorf("reader.BeginTx call count = %d, want 1", reader.beginTxN)
	}
	if writer.beginTxN != 0 {
		t.Errorf("writer.BeginTx call count = %d, want 0", writer.beginTxN)
	}
	if reader.lastOpts == nil || !reader.lastOpts.ReadOnly {
		t.Errorf("reader received opts = %#v, want ReadOnly=true", reader.lastOpts)
	}
}

// TestBeginTx_ReadWrite_RoutesToWriter confirms that the default (non
// read-only) BeginTx path stays on the writer pool. This guards mutation
// semantics: a stray BeginTx without opts.ReadOnly must never silently move
// to the replica.
func TestBeginTx_ReadWrite_RoutesToWriter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts *sql.TxOptions
	}{
		{"nil opts", nil},
		{"explicit non-readonly", &sql.TxOptions{ReadOnly: false}},
		{"isolation only, not readonly", &sql.TxOptions{Isolation: sql.LevelSerializable}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reader := &fakeDriver{label: "reader"}
			writer := &fakeDriver{label: "writer"}

			driver := db.NewMultiDriverWithDrivers(reader, writer)

			if _, err := driver.BeginTx(context.Background(), tc.opts); err != nil {
				t.Fatalf("BeginTx returned error: %v", err)
			}

			if writer.beginTxN != 1 {
				t.Errorf("writer.BeginTx call count = %d, want 1", writer.beginTxN)
			}
			if reader.beginTxN != 0 {
				t.Errorf("reader.BeginTx call count = %d, want 0", reader.beginTxN)
			}
		})
	}
}

// TestTx_AlwaysRoutesToWriter pins down the explicit guarantee from Step 8.1:
// Tx(ctx) carries no opts and must continue to default to the writer pool.
// Existing call sites still using client.Tx(ctx) for mutations must not
// regress to the reader.
func TestTx_AlwaysRoutesToWriter(t *testing.T) {
	t.Parallel()

	reader := &fakeDriver{label: "reader"}
	writer := &fakeDriver{label: "writer"}

	driver := db.NewMultiDriverWithDrivers(reader, writer)

	if _, err := driver.Tx(context.Background()); err != nil {
		t.Fatalf("Tx returned error: %v", err)
	}

	if writer.txCalled != 1 {
		t.Errorf("writer.Tx call count = %d, want 1", writer.txCalled)
	}
	if reader.txCalled != 0 {
		t.Errorf("reader.Tx call count = %d, want 0", reader.txCalled)
	}
}

// TestBeginTx_PropagatesError checks the error path for the chosen pool so a
// future refactor doesn't accidentally drop or wrap the underlying driver
// error.
func TestBeginTx_PropagatesError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	reader := &fakeDriver{label: "reader", beginTxErr: wantErr}
	writer := &fakeDriver{label: "writer"}

	driver := db.NewMultiDriverWithDrivers(reader, writer)

	_, err := driver.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if !errors.Is(err, wantErr) {
		t.Fatalf("BeginTx error = %v, want %v", err, wantErr)
	}
}
