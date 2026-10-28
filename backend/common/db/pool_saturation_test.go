package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pool-saturation probe below needs no live database: database/sql
// enforces SetMaxOpenConns above the driver, so a stub driver that hands out
// connections for free is enough to observe what a caller experiences when
// every connection is already checked out.

type stubDriver struct{ opened atomic.Int64 }

func (d *stubDriver) Open(string) (driver.Conn, error) {
	d.opened.Add(1)
	return &stubConn{}, nil
}

type stubConn struct{}

func (*stubConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*stubConn) Close() error                        { return nil }
func (*stubConn) Begin() (driver.Tx, error)           { return stubTx{}, nil }

type stubTx struct{}

func (stubTx) Commit() error   { return nil }
func (stubTx) Rollback() error { return nil }

// driverSeq numbers the registered driver names. sql.Register panics on a
// duplicate and there is no way to unregister, so a fixed name per probe
// would make `go test -count=2` (or any second run of these tests in one
// process) fail on registration instead of on an assertion.
var driverSeq atomic.Int64

// openSaturatedPool returns a pool of exactly maxOpen connections with all
// of them checked out, and the transactions holding them.
func openSaturatedPool(t *testing.T, maxOpen int) (*sql.DB, *stubDriver, []*sql.Tx) {
	t.Helper()

	d := &stubDriver{}
	name := fmt.Sprintf("pyck-pool-probe-%s-%d", t.Name(), driverSeq.Add(1))
	sql.Register(name, d)

	pool, err := sql.Open(name, "")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, pool.Close()) })
	pool.SetMaxOpenConns(maxOpen)

	held := make([]*sql.Tx, 0, maxOpen)
	for range maxOpen {
		tx, err := pool.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		held = append(held, tx)
	}
	require.Equal(t, int64(maxOpen), d.opened.Load(), "the pool must be saturated before the probe")

	return pool, d, held
}

// A caller that asks for a connection while every one of them is checked out
// does NOT get an error: database/sql parks it until a connection is
// returned or its context ends. This is why a mutation that holds a writer
// connection across a 25 s wait is a fleet-wide problem rather than a local
// one: the requests behind it queue silently instead of failing fast, and
// the only thing that ends their wait is the client's own deadline.
func TestPool_SaturatedPoolParksTheCallerUntilItsContextEnds(t *testing.T) {
	t.Parallel()

	pool, d, _ := openSaturatedPool(t, 2)

	const patience = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), patience)
	defer cancel()

	start := time.Now()
	tx, err := pool.BeginTx(ctx, nil)
	waited := time.Since(start)

	assert.Nil(t, tx)
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the saturated pool must park the caller until its context ends, not refuse it with a pool error")
	assert.GreaterOrEqual(t, waited, patience,
		"the caller waited for a connection rather than failing fast")
	assert.Equal(t, int64(2), d.opened.Load(),
		"SetMaxOpenConns is the ceiling: no extra connection is opened for the parked caller")
}

// A cancelled request unparks immediately, which is the one way a queued
// caller gets its goroutine back: nothing in the pool times a waiter out.
func TestPool_ParkedCallerUnparksOnCancel(t *testing.T) {
	t.Parallel()

	pool, _, _ := openSaturatedPool(t, 2)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := pool.BeginTx(ctx, nil)

	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 5*time.Second, "the cancelled caller must not stay parked")
}

// The parked caller is served the moment a holder gives its connection
// back. That is the whole reason the hold time of one mutation is the
// latency of every request behind it.
func TestPool_ReturnedConnectionServesTheParkedCaller(t *testing.T) {
	t.Parallel()

	pool, d, held := openSaturatedPool(t, 2)

	admitted := make(chan error, 1)
	go func() {
		tx, err := pool.BeginTx(t.Context(), nil)
		if err == nil {
			err = tx.Rollback()
		}
		admitted <- err
	}()

	// Nothing can be served while both connections are held.
	select {
	case err := <-admitted:
		t.Fatalf("a caller was served by a saturated pool: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, held[0].Rollback())

	select {
	case err := <-admitted:
		require.NoError(t, err, "the freed connection must serve the parked caller")
	case <-time.After(5 * time.Second):
		t.Fatal("the parked caller was not served after a connection was returned")
	}

	assert.Equal(t, int64(2), d.opened.Load(),
		"the handed-over connection is reused, not a third one")
}
