package services_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// fakeDependency is a health check the test can switch off and on.
type fakeDependency struct {
	down   atomic.Bool
	probes atomic.Int32
}

func (f *fakeDependency) check() services.HealthCheck {
	return services.HealthCheck{Name: "fake", Check: func(context.Context) error {
		f.probes.Add(1)

		if f.down.Load() {
			return errors.New("fake dependency is down")
		}

		return nil
	}}
}

// pauseCfg is fastCfg with a health gate polling every few milliseconds.
func pauseCfg(dep *fakeDependency) services.ConsumerConfig {
	cfg := fastCfg()
	cfg.Health = services.HealthConfig{
		Checks:    []services.HealthCheck{dep.check()},
		Interval:  30 * time.Millisecond,
		Timeout:   time.Second,
		ResumeMin: 20 * time.Millisecond,
		ResumeMax: 50 * time.Millisecond,
	}

	return cfg
}

func delivered(t *testing.T, e *natsEnv) (stream, redelivered uint64) {
	t.Helper()

	c, err := e.js.Consumer(context.Background(), consumerStream, services.DefaultConsumerDurable)
	require.NoError(t, err)

	info, err := c.Info(context.Background())
	require.NoError(t, err)

	return info.Delivered.Stream, uint64(info.NumRedelivered)
}

var errOutage = serviceerror.NewUnavailable("temporal is unavailable")

func TestIsDependencyOutage(t *testing.T) {
	t.Parallel()

	outages := map[string]error{
		"temporal unavailable":      errOutage,
		"temporal deadline":         serviceerror.NewDeadlineExceeded("slow"),
		"grpc unavailable":          status.Error(codes.Unavailable, "down"),
		"grpc deadline":             status.Error(codes.DeadlineExceeded, "slow"),
		"wrapped":                   fmt.Errorf("failed to signal: %w", errOutage),
		"joined":                    errors.Join(errors.New("other"), errOutage),
		"context deadline":          context.DeadlineExceeded,
		"db bad conn":               driver.ErrBadConn,
		"net op error":              &net.OpError{Op: "dial", Err: errors.New("connection refused")},
		"pg connection exception":   &pgconn.PgError{Code: "08006"},
		"pg admin shutdown":         &pgconn.PgError{Code: "57P01"},
		"pg too many connections":   &pgconn.PgError{Code: "53300"},
		"pg connect error":          &pgconn.ConnectError{Config: &pgconn.Config{}},
		"wrapped db outage, joined": fmt.Errorf("load: %w", errors.Join(errors.New("x"), driver.ErrBadConn)),
	}
	for name, err := range outages {
		assert.True(t, services.IsDependencyOutage(err), name)
	}

	others := map[string]error{
		"nil":                  nil,
		"plain":                errors.New("boom"),
		"temporal not found":   serviceerror.NewNotFound("gone"),
		"invalid argument":     serviceerror.NewInvalidArgument("bad"),
		"permission denied":    status.Error(codes.PermissionDenied, "no"),
		"context canceled":     context.Canceled,
		"pg unique violation":  &pgconn.PgError{Code: "23505"},
		"pg serialization":     &pgconn.PgError{Code: "40001"},
		"permanent and so not": services.Permanent(errors.New("bad")),
		"rate limit is held per event, not a pause": serviceerror.NewResourceExhausted(enumspb.RESOURCE_EXHAUSTED_CAUSE_RPS_LIMIT, "namespace rate limit"),
	}
	for name, err := range others {
		assert.False(t, services.IsDependencyOutage(err), name)
	}
}

func TestIsRateLimited(t *testing.T) {
	t.Parallel()

	limited := map[string]error{
		"temporal resource exhausted": serviceerror.NewResourceExhausted(enumspb.RESOURCE_EXHAUSTED_CAUSE_RPS_LIMIT, "namespace rate limit"),
		"grpc resource exhausted":     status.Error(codes.ResourceExhausted, "limit"),
		"wrapped":                     fmt.Errorf("failed to signal: %w", serviceerror.NewResourceExhausted(enumspb.RESOURCE_EXHAUSTED_CAUSE_RPS_LIMIT, "x")),
		"joined":                      errors.Join(errors.New("other"), status.Error(codes.ResourceExhausted, "limit")),
	}
	for name, err := range limited {
		assert.True(t, services.IsRateLimited(err), name)
	}

	others := map[string]error{
		"nil":            nil,
		"plain":          errors.New("boom"),
		"unavailable":    errOutage,
		"invalid":        serviceerror.NewInvalidArgument("bad"),
		"permanent":      services.Permanent(status.Error(codes.ResourceExhausted, "limit")),
		"grpc not found": status.Error(codes.NotFound, "gone"),
	}
	for name, err := range others {
		assert.False(t, services.IsRateLimited(err), name)
	}
}

// TestPause_RateLimitHoldsEventWithoutBurningAttemptsOrPausing: Temporal
// answering ResourceExhausted (a namespace rate limit) is not an outage, since
// the health probes pass. The event is held and retried, costs no delivery
// attempt and the router never pauses.
//
//nolint:paralleltest // reads the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_RateLimitHoldsEventWithoutBurningAttemptsOrPausing(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	var calls atomic.Int32

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 2
	cfg.Backoff = []time.Duration{20 * time.Millisecond}

	before := testutil.ToFloat64(services.EventsGivenUp())

	// Rate limited on the first 5 attempts: more than MaxDeliver, so a NAK
	// based retry would have given the event up.
	e.router(cfg, func(context.Context, *nats.Msg) error {
		if calls.Add(1) <= 5 {
			return serviceerror.NewResourceExhausted(enumspb.RESOURCE_EXHAUSTED_CAUSE_RPS_LIMIT, "namespace rate limit")
		}

		return nil
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() == 6 }, 5*time.Second, 10*time.Millisecond,
		"the rate-limited event is retried until Temporal lets it through")
	e.settled()

	stream, redelivered := delivered(t, e)
	assert.EqualValues(t, 1, stream)
	assert.Zero(t, redelivered, "the rate limit cost no delivery attempt")
	assert.InDelta(t, before, testutil.ToFloat64(services.EventsGivenUp()), 0.001, "not given up")
	assert.InDelta(t, 0, testutil.ToFloat64(services.RouterPaused()), 0.001, "a rate limit does not pause the router")
}

// TestPause_RateLimitHoldIsBounded: an event that stays ResourceExhausted
// past the hold budget (for example one the gRPC size limit refuses) counts
// as a failed delivery again, so it ends in the give-up path instead of
// holding a MaxAckPending slot forever.
//
//nolint:paralleltest // reads the process-global give-up counter that parallel tests would also move
func TestPause_RateLimitHoldIsBounded(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 2
	cfg.Backoff = []time.Duration{20 * time.Millisecond}
	cfg.Health.RateLimitHold = 100 * time.Millisecond

	before := testutil.ToFloat64(services.EventsGivenUp())

	e.router(cfg, func(context.Context, *nats.Msg) error {
		return status.Error(codes.ResourceExhausted, "message larger than max")
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(services.EventsGivenUp()) == before+1
	}, 10*time.Second, 20*time.Millisecond)
	e.settled()
}

func TestIsTemporalUnavailable(t *testing.T) {
	t.Parallel()

	yes := map[string]error{
		"temporal unavailable": errOutage,
		"temporal deadline":    serviceerror.NewDeadlineExceeded("slow"),
		"grpc unavailable":     status.Error(codes.Unavailable, "down"),
		"grpc deadline":        status.Error(codes.DeadlineExceeded, "slow"),
		"wrapped":              fmt.Errorf("failed to signal: %w", errOutage),
		"joined":               errors.Join(errors.New("other"), errOutage),
	}
	for name, err := range yes {
		assert.True(t, services.IsTemporalUnavailable(err), name)
	}

	no := map[string]error{
		"nil":              nil,
		"plain":            errors.New("boom"),
		"context deadline": context.DeadlineExceeded,
		"db bad conn":      driver.ErrBadConn,
		"pg outage":        &pgconn.PgError{Code: "57P01"},
		"permanent":        services.Permanent(errOutage),
		"resource":         status.Error(codes.ResourceExhausted, "limit"),
	}
	for name, err := range no {
		assert.False(t, services.IsTemporalUnavailable(err), name)
	}
}

// TestPause_TemporalUnavailableWithPassingProbesIsHeldPerEvent: Temporal's
// frontend keeps answering the gRPC health check while its own database or
// history is down, so every start and signal returns Unavailable but the
// probes pass. The event is held like a rate limit: no delivery attempt is
// used, and the router does not pause.
//
//nolint:paralleltest // reads the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_TemporalUnavailableWithPassingProbesIsHeldPerEvent(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	var calls atomic.Int32

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 2
	cfg.Backoff = []time.Duration{20 * time.Millisecond}

	before := testutil.ToFloat64(services.EventsGivenUp())

	e.router(cfg, func(context.Context, *nats.Msg) error {
		if calls.Add(1) <= 5 {
			return serviceerror.NewUnavailable("persistence is down")
		}

		return nil
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() == 6 }, 5*time.Second, 10*time.Millisecond,
		"the event is retried until Temporal answers again")
	e.settled()

	stream, redelivered := delivered(t, e)
	assert.EqualValues(t, 1, stream)
	assert.Zero(t, redelivered, "holding cost no delivery attempt")
	assert.InDelta(t, before, testutil.ToFloat64(services.EventsGivenUp()), 0.001, "not given up")
	assert.InDelta(t, 0, testutil.ToFloat64(services.RouterPaused()), 0.001, "probes pass, so the router does not pause")
}

// TestPause_TemporalUnavailableHoldIsBounded: an event whose Temporal calls
// keep failing with Unavailable while the probes pass is held for at most
// RateLimitHold. After that it counts as an ordinary failed delivery and ends
// in the give-up path.
//
//nolint:paralleltest // reads the process-global give-up counter that parallel tests would also move
func TestPause_TemporalUnavailableHoldIsBounded(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 2
	cfg.Backoff = []time.Duration{20 * time.Millisecond}
	cfg.Health.RateLimitHold = 100 * time.Millisecond

	before := testutil.ToFloat64(services.EventsGivenUp())

	e.router(cfg, func(context.Context, *nats.Msg) error {
		return status.Error(codes.Unavailable, "persistence is down")
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(services.EventsGivenUp()) == before+1
	}, 10*time.Second, 20*time.Millisecond)
	e.settled()
}

// TestPause_OutageHoldsEventWithoutBurningAttempts: a dependency outage
// reported by a handler pauses the router. Nothing more is fetched while the
// dependency is down, the event is not given up even though the outage spans
// many more NAK delays than MaxDeliver, and it is handled once the dependency
// is back, still on its first delivery.
//
//nolint:paralleltest // reads and sets the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_OutageHoldsEventWithoutBurningAttempts(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	var calls atomic.Int32

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 2
	cfg.Backoff = []time.Duration{20 * time.Millisecond}

	before := testutil.ToFloat64(services.EventsGivenUp())

	e.router(cfg, func(context.Context, *nats.Msg) error {
		calls.Add(1)

		if dep.down.Load() {
			return errOutage
		}

		return nil
	})

	dep.down.Store(true)
	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return testutil.ToFloat64(services.RouterPaused()) == 1 }, 5*time.Second, 5*time.Millisecond)

	e.publish(mutationSubject()) // arrive while paused
	e.publish(mutationSubject())

	time.Sleep(2500 * time.Millisecond) // many NAK delays and AckWaits; MaxDeliver is 2

	// A pull request already waiting at the server when the gate closed may
	// take one more event; it is held, never handled. No other event is taken.
	stream, redelivered := delivered(t, e)
	assert.LessOrEqual(t, stream, uint64(2), "at most the one event of an already-pending request is fetched while paused")
	assert.Zero(t, redelivered, "the held events were not redelivered")
	assert.EqualValues(t, 1, calls.Load(), "no handler runs while the dependency is down")

	dep.down.Store(false)

	require.Eventually(t, func() bool { return calls.Load() == 4 }, 5*time.Second, 10*time.Millisecond,
		"the held event is retried and the waiting ones are handled after recovery")
	e.settled()

	stream, redelivered = delivered(t, e)
	assert.EqualValues(t, 3, stream)
	assert.Zero(t, redelivered, "the outage cost no delivery attempt")
	assert.InDelta(t, before, testutil.ToFloat64(services.EventsGivenUp()), 0.001, "not given up")
	assert.InDelta(t, 0, testutil.ToFloat64(services.RouterPaused()), 0.001, "resumed")
}

// TestPause_PeriodicCheckFailurePauses: a failing periodic check pauses the
// router before any handler fails, and recovery resumes it.
//
//nolint:paralleltest // reads and sets the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_PeriodicCheckFailurePauses(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	var calls atomic.Int32

	e.router(pauseCfg(dep), func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return nil
	})

	dep.down.Store(true)
	require.Eventually(t, func() bool { return testutil.ToFloat64(services.RouterPaused()) == 1 }, 5*time.Second, 5*time.Millisecond)

	e.publish(mutationSubject())
	time.Sleep(500 * time.Millisecond)
	assert.Zero(t, calls.Load(), "nothing is handled while a check fails")

	dep.down.Store(false)
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	e.settled()
	assert.InDelta(t, 0, testutil.ToFloat64(services.RouterPaused()), 0.001)
}

// TestPause_TransientErrorStillCountsTowardMaxDeliver: an ordinary failure is
// not an outage, so it burns attempts and is given up on.
//
//nolint:paralleltest // reads and sets the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_TransientErrorStillCountsTowardMaxDeliver(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	var calls atomic.Int32

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 3
	cfg.Backoff = []time.Duration{20 * time.Millisecond}

	before := testutil.ToFloat64(services.EventsGivenUp())

	e.router(cfg, func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return errTransient
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() == 3 }, 5*time.Second, 10*time.Millisecond)
	e.settled()
	time.Sleep(500 * time.Millisecond)

	assert.EqualValues(t, 3, calls.Load())
	assert.InDelta(t, before+1, testutil.ToFloat64(services.EventsGivenUp()), 0.001)
	assert.InDelta(t, 0, testutil.ToFloat64(services.RouterPaused()), 0.001, "a plain failure does not pause")
}

// TestPause_OutageErrorWithHealthyProbesCounts: an error that looks like an
// outage while every probe passes is specific to the event (a slow call, a
// namespace problem). It must not be held forever, so it burns attempts. (A
// Temporal Unavailable is different: it is held per event, bounded by
// RateLimitHold, see TestPause_TemporalUnavailableHoldIsBounded.)
//
//nolint:paralleltest // reads and sets the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_OutageErrorWithHealthyProbesCounts(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	var calls atomic.Int32

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 3
	cfg.Backoff = []time.Duration{20 * time.Millisecond}

	before := testutil.ToFloat64(services.EventsGivenUp())

	e.router(cfg, func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return calls.Load() == 3 }, 5*time.Second, 10*time.Millisecond)
	e.settled()
	time.Sleep(500 * time.Millisecond)

	assert.EqualValues(t, 3, calls.Load())
	assert.InDelta(t, before+1, testutil.ToFloat64(services.EventsGivenUp()), 0.001)
}

// TestPause_ReleaseOnTheLastDeliveryIsRedeliveredAndGivenUp: Stop NAKs a held
// event, and a NAK counts as a delivery. If the server enforced MaxDeliver, an
// event released on what would be its last delivery would never be seen again:
// no give-up counter, no log, no recorded outcome. The consumer therefore has
// no server-side limit and the router's own check gives the event up.
//
//nolint:paralleltest // reads and sets the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_ReleaseOnTheLastDeliveryIsRedeliveredAndGivenUp(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	var first atomic.Int32

	cfg := pauseCfg(dep)
	cfg.MaxDeliver = 2
	cfg.Backoff = []time.Duration{20 * time.Millisecond}

	before := testutil.ToFloat64(services.EventsGivenUp())

	r1 := e.router(cfg, func(context.Context, *nats.Msg) error {
		if first.Add(1) == 1 {
			return errTransient // delivery 1: NAK
		}

		dep.down.Store(true) // delivery 2, the last: held by an outage

		return errOutage
	})

	e.publish(mutationSubject())

	require.Eventually(t, func() bool { return testutil.ToFloat64(services.RouterPaused()) == 1 }, 5*time.Second, 5*time.Millisecond,
		"the event is held on its last delivery")

	r1.Stop() // releases the held event with a NAK, which counts as a delivery
	dep.down.Store(false)

	var second atomic.Int32

	e.router(cfg, func(context.Context, *nats.Msg) error {
		second.Add(1)
		return errTransient
	})

	require.Eventually(t, func() bool { return second.Load() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the released event is redelivered although it was on its last delivery")
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(services.EventsGivenUp()) == before+1
	}, 5*time.Second, 10*time.Millisecond, "and given up through the router, which counts it")
	e.settled()
}

// TestConsumer_HasNoServerSideMaxDeliver: the durable is created, and an
// existing one updated in place, with MaxDeliver -1 (the router enforces its
// own limit).
func TestConsumer_HasNoServerSideMaxDeliver(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)

	_, err := e.js.CreateOrUpdateConsumer(context.Background(), consumerStream, jetstream.ConsumerConfig{
		Durable:        services.DefaultConsumerDurable,
		FilterSubjects: []string{consumerStream + ".*.crud.*.*.*.*"},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        time.Second,
		MaxDeliver:     20,
	})
	require.NoError(t, err)

	e.router(fastCfg(), func(context.Context, *nats.Msg) error { return nil })

	c, err := e.js.Consumer(context.Background(), consumerStream, services.DefaultConsumerDurable)
	require.NoError(t, err)

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, -1, info.Config.MaxDeliver, "an existing durable is updated in place")
}

// TestPause_StopDoesNotHangOnAHeldEvent: Stop while the router is paused
// releases the held event instead of waiting for the dependency.
//
//nolint:paralleltest // reads and sets the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_StopDoesNotHangOnAHeldEvent(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}
	dep.down.Store(true)

	var calls atomic.Int32

	r := e.router(pauseCfg(dep), func(context.Context, *nats.Msg) error {
		calls.Add(1)
		return errOutage
	})

	e.publish(mutationSubject())
	require.Eventually(t, func() bool { return calls.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return testutil.ToFloat64(services.RouterPaused()) == 1 }, 5*time.Second, 5*time.Millisecond)

	stopped := make(chan struct{})

	go func() { r.Stop(); close(stopped) }()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung on a held event")
	}
}

func TestSQLHealthCheck(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	check := services.SQLHealthCheck("db", db)
	require.NoError(t, check.Check(context.Background()))

	require.NoError(t, db.Close())
	assert.Error(t, check.Check(context.Background()), "a closed database is unhealthy")
}

type stubTemporal struct{ err error }

func (s stubTemporal) CheckHealth(context.Context, *temporalclient.CheckHealthRequest) (*temporalclient.CheckHealthResponse, error) {
	return &temporalclient.CheckHealthResponse{}, s.err
}

func TestTemporalHealthCheck(t *testing.T) {
	t.Parallel()

	require.NoError(t, services.TemporalHealthCheck("temporal", stubTemporal{}).Check(context.Background()))
	assert.Error(t, services.TemporalHealthCheck("temporal", stubTemporal{err: errOutage}).Check(context.Background()))
}

// syncBuffer is a log sink several goroutines write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return strings.Count(b.buf.String(), s)
}

// A router that stays paused says so again and again, not once: a probe that
// can never pass (blocked by an ingress, misconfigured) is otherwise a silent
// delivery stop. The router recovers with the dependency. The
// router is not part of /health/ready: it pauses and holds on its own, and a
// Temporal outage must not take the whole service out of rotation.
//
//nolint:paralleltest // reads and sets the process-global routerPaused gauge and give-up counter that parallel tests would also move
func TestPause_StillPausedIsLoggedRepeatedly(t *testing.T) {
	e := newNatsEnv(t)
	dep := &fakeDependency{}

	cfg := pauseCfg(dep)
	cfg.Health.PausedWarnInterval = 100 * time.Millisecond

	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	js, err := jetstream.New(e.connect())
	require.NoError(t, err)

	r := services.NewSignalRouter(nil, services.SignalRouterConfig{JetstreamClient: js, StreamName: consumerStream, Consumer: cfg})
	r.SetDispatch(func(context.Context, *nats.Msg) error { return nil })
	require.NoError(t, r.Start(ctx))
	t.Cleanup(r.Stop)

	assert.False(t, r.Paused(), "not paused while healthy")

	dep.down.Store(true)
	require.Eventually(t, func() bool { return r.Paused() }, 5*time.Second, 10*time.Millisecond, "paused")

	require.Eventually(t, func() bool { return logs.count("still paused") >= 3 }, 5*time.Second, 20*time.Millisecond,
		"a warning per interval while the probe keeps failing")

	dep.down.Store(false)
	require.Eventually(t, func() bool { return !r.Paused() }, 5*time.Second, 10*time.Millisecond, "resumed")

	n := logs.count("still paused")
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, n, logs.count("still paused"), "no warnings once resumed")
}
