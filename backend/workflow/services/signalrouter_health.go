package services

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	// DefaultHealthInterval is how often a healthy router probes its
	// dependencies.
	DefaultHealthInterval = 5 * time.Second

	// DefaultHealthTimeout bounds one probe of one dependency.
	DefaultHealthTimeout = 2 * time.Second

	// DefaultResumeMin and DefaultResumeMax bound the probe interval while
	// the router is paused: it starts at the minimum and backs off to the
	// maximum.
	DefaultResumeMin = time.Second
	DefaultResumeMax = 5 * time.Second

	// DefaultRateLimitHold is how long one event is held, for a Temporal rate
	// limit or an Unavailable the probes cannot see, before its error counts
	// as a failed delivery. It stays well below AckWait times
	// MaxDeliver so a stuck event still ends in the give-up path.
	DefaultRateLimitHold = 15 * time.Minute

	// DefaultPausedWarnInterval is how often a router that stays paused logs
	// that it still is.
	DefaultPausedWarnInterval = time.Minute
)

// HealthCheck probes one dependency of the router.
type HealthCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// HealthConfig tunes the router's health gate. With no Checks there is no
// gate: the router never pauses.
type HealthConfig struct {
	// Checks are the dependencies the router cannot work without: the
	// workflow service's database and Temporal.
	Checks []HealthCheck
	// Interval is the probe period while healthy.
	Interval time.Duration
	// Timeout bounds each probe.
	Timeout time.Duration
	// ResumeMin and ResumeMax bound the probe period while paused.
	ResumeMin time.Duration
	ResumeMax time.Duration
	// PausedWarnInterval is how often a router that stays paused logs that it
	// still is.
	PausedWarnInterval time.Duration
	// RateLimitHold is how long one event is held, un-acked and retried after
	// ResumeMin..ResumeMax, while Temporal answers ResourceExhausted, or
	// Unavailable or DeadlineExceeded with every probe passing (its own
	// database or history is down). Past it the error counts as a failed
	// delivery.
	RateLimitHold time.Duration
}

func (h HealthConfig) withDefaults() HealthConfig {
	if h.Interval <= 0 {
		h.Interval = DefaultHealthInterval
	}

	if h.Timeout <= 0 {
		h.Timeout = DefaultHealthTimeout
	}

	if h.ResumeMin <= 0 {
		h.ResumeMin = DefaultResumeMin
	}

	if h.PausedWarnInterval <= 0 {
		h.PausedWarnInterval = DefaultPausedWarnInterval
	}

	if h.RateLimitHold <= 0 {
		h.RateLimitHold = DefaultRateLimitHold
	}

	if h.ResumeMax < h.ResumeMin {
		h.ResumeMax = max(h.ResumeMin, DefaultResumeMax)
	}

	return h
}

// SQLHealthCheck probes a database with a SELECT 1 round trip.
func SQLHealthCheck(name string, db *sql.DB) HealthCheck {
	return HealthCheck{Name: name, Check: func(ctx context.Context) error {
		_, err := db.ExecContext(ctx, "SELECT 1")
		return err
	}}
}

// TemporalHealthCheck probes Temporal through the client's gRPC health check.
// The client is the router's root client, whose connection every namespace
// client shares, so this tests the connection the router actually uses.
func TemporalHealthCheck(name string, c interface {
	CheckHealth(ctx context.Context, request *temporalclient.CheckHealthRequest) (*temporalclient.CheckHealthResponse, error)
},
) HealthCheck {
	return HealthCheck{Name: name, Check: func(ctx context.Context) error {
		_, err := c.CheckHealth(ctx, &temporalclient.CheckHealthRequest{})
		return err
	}}
}

var routerPaused = promauto.NewGauge( //nolint:gochecknoglobals
	prometheus.GaugeOpts{
		Name: "workflow_signal_router_paused",
		Help: "1 while the signal router has stopped fetching events because Temporal or the database is unreachable, else 0",
	},
)

// healthGate pauses the router while a dependency is down. It is tripped by a
// failing periodic probe or by a handler error that looks like an outage, and
// reopens once every probe passes.
type healthGate struct {
	cfg HealthConfig

	mu      sync.Mutex
	tripped bool
	// trippedSince is when the gate last closed.
	trippedSince time.Time
	openCh       chan struct{} // closed on resume
	kick         chan struct{} // wakes the monitor when tripped by a handler
}

func newHealthGate(cfg HealthConfig) *healthGate {
	return &healthGate{cfg: cfg, openCh: make(chan struct{}), kick: make(chan struct{}, 1)}
}

// enabled reports whether there is anything to probe.
func (g *healthGate) enabled() bool { return len(g.cfg.Checks) > 0 }

// probe runs every check and returns the failures, joined.
func (g *healthGate) probe(ctx context.Context) error {
	var errs []error

	for _, c := range g.cfg.Checks {
		cctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
		err := c.Check(cctx)

		cancel()

		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
		}
	}

	return errors.Join(errs...)
}

// trip closes the gate. Tripping an already closed gate is a no-op.
func (g *healthGate) trip(ctx context.Context, reason error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.tripped {
		return
	}

	g.tripped = true
	g.trippedSince = time.Now()
	g.openCh = make(chan struct{})
	routerPaused.Set(1)

	log.ForContext(ctx).Warn().Err(reason).Msg("signal router paused: a dependency is down, events wait in the stream")

	select {
	case g.kick <- struct{}{}:
	default:
	}
}

func (g *healthGate) resume(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.tripped {
		return
	}

	g.tripped = false
	routerPaused.Set(0)
	close(g.openCh)

	log.ForContext(ctx).Info().Msg("signal router resumed: every dependency is healthy again")
}

func (g *healthGate) trippedAt() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.trippedSince
}

func (g *healthGate) isTripped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.tripped
}

// waitOpen blocks while the gate is closed. It returns false if stop or ctx
// ended first.
func (g *healthGate) waitOpen(ctx context.Context, stop <-chan struct{}) bool {
	for {
		g.mu.Lock()
		tripped, openCh := g.tripped, g.openCh
		g.mu.Unlock()

		if !tripped {
			return true
		}

		select {
		case <-openCh:
		case <-stop:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// confirmOutage decides whether a handler error means the router should hold
// the event instead of counting an attempt. It must look like an outage, and
// a probe must agree: an outage-looking error while every dependency probes
// healthy belongs to the event (a slow fan-out, one namespace), and holding
// it would loop forever. A failing probe trips the gate.
func (g *healthGate) confirmOutage(ctx context.Context, err error) bool {
	if !g.enabled() || !isDependencyOutage(err) {
		return false
	}

	if g.isTripped() {
		return true
	}

	if probeErr := g.probe(ctx); probeErr != nil {
		g.trip(ctx, fmt.Errorf("handler failed with %w; probe: %w", err, probeErr))

		return true
	}

	return false
}

// run probes until stop: every Interval while healthy, and from ResumeMin
// backing off to ResumeMax while tripped.
func (g *healthGate) run(ctx context.Context, stop <-chan struct{}) {
	defer func() {
		g.mu.Lock()
		g.tripped = false
		routerPaused.Set(0)
		g.mu.Unlock()
	}()

	delay := g.cfg.ResumeMin

	var lastWarn time.Time // when a paused router last said it still is

	for {
		tripped := g.isTripped()

		wait := g.cfg.Interval
		if tripped {
			wait = delay
		} else {
			delay = g.cfg.ResumeMin
			lastWarn = time.Time{}
		}

		timer := time.NewTimer(wait)

		select {
		case <-stop:
			timer.Stop()
			return
		case <-ctx.Done():
			timer.Stop()
			return
		case <-g.kick:
			timer.Stop()
			continue
		case <-timer.C:
		}

		err := g.probe(ctx)

		switch {
		case tripped && err == nil:
			g.resume(ctx)
		case tripped:
			delay = min(delay*2, g.cfg.ResumeMax)

			// A probe that can never pass (blocked by an ingress or authorizer,
			// misconfigured) would otherwise be a delivery stop that logs once.
			if lastWarn.IsZero() || time.Since(lastWarn) >= g.cfg.PausedWarnInterval {
				lastWarn = time.Now()

				log.ForContext(ctx).Warn().Err(err).
					Dur("paused_for", time.Since(g.trippedAt())).
					Msg("signal router still paused: a dependency probe keeps failing, events wait in the stream")
			}
		case err != nil:
			g.trip(ctx, err)
		}
	}
}

// isDependencyOutage reports whether err says Temporal or the database is
// unreachable or too slow to answer: a Temporal Unavailable or
// DeadlineExceeded, the same gRPC codes, a deadline, a network or connection
// error, or a Postgres connection-class error. A joined error is an outage if
// any part is, since redelivering the event while the dependency is down
// would fail the same way. A permanent error never is.
func isDependencyOutage(err error) bool {
	if err == nil || isPermanent(err) {
		return false
	}

	var (
		unavailable *serviceerror.Unavailable
		deadline    *serviceerror.DeadlineExceeded
		netErr      net.Error
		pgErr       *pgconn.PgError
		connectErr  *pgconn.ConnectError
	)

	switch {
	case errors.As(err, &unavailable), errors.As(err, &deadline):
		return true
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, driver.ErrBadConn),
		errors.Is(err, sql.ErrConnDone),
		errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &connectErr), errors.As(err, &netErr):
		return true
	case errors.As(err, &pgErr):
		return isPostgresOutage(pgErr.Code)
	}

	if s, ok := status.FromError(err); ok {
		return s.Code() == codes.Unavailable || s.Code() == codes.DeadlineExceeded
	}

	return false
}

// isRateLimited reports whether err says Temporal is shedding load: a
// ResourceExhausted from a namespace or system rate limit, as a serviceerror
// or the gRPC code. It is not an outage: CheckHealth still passes, so it does
// not pause the router and is held per event instead (see process). A
// permanent error never is.
func isRateLimited(err error) bool {
	if err == nil || isPermanent(err) {
		return false
	}

	var exhausted *serviceerror.ResourceExhausted
	if errors.As(err, &exhausted) {
		return true
	}

	s, ok := status.FromError(err)

	return ok && s.Code() == codes.ResourceExhausted
}

// isTemporalUnavailable reports whether err is a Temporal Unavailable or
// DeadlineExceeded, as a serviceerror or the gRPC code. That is what starts
// and signals return while Temporal's own database or history is down, which
// CheckHealth cannot see (the frontend keeps answering SERVING). When the
// probes pass, the router holds such an event per event, like a rate limit
// (see process). A permanent error never is.
func isTemporalUnavailable(err error) bool {
	if err == nil || isPermanent(err) {
		return false
	}

	var (
		unavailable *serviceerror.Unavailable
		deadline    *serviceerror.DeadlineExceeded
	)

	if errors.As(err, &unavailable) || errors.As(err, &deadline) {
		return true
	}

	s, ok := status.FromError(err)

	return ok && (s.Code() == codes.Unavailable || s.Code() == codes.DeadlineExceeded)
}

// isPostgresOutage reports whether a SQLSTATE means the server is down,
// shutting down or out of connections, as opposed to a problem with the
// statement.
func isPostgresOutage(code string) bool {
	switch code {
	case "53300", // too_many_connections
		"57P01", // admin_shutdown
		"57P02", // crash_shutdown
		"57P03": // cannot_connect_now
		return true
	}

	return strings.HasPrefix(code, "08") // connection_exception
}
