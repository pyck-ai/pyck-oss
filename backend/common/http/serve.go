package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"sync/atomic"
	"time"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// HealthChecker reports whether a service dependency is usable. It is a
// structural copy of handlers.HealthCheckInterface; it lives here because
// handlers imports this package, so this package cannot import handlers.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// readinessCheckTimeout bounds each readiness pass. It must stay comfortably
// below the Kubernetes probe's timeoutSeconds (3s) and comfortably above p99
// SELECT 1 on the dedicated health pool.
const readinessCheckTimeout = 2 * time.Second

// ServerOptions bounds the graceful-shutdown phases of Serve.
type ServerOptions struct {
	// DrainTimeout caps how long Shutdown waits for in-flight requests
	// after the listener closes. Requests still running at the deadline are
	// abandoned (their connections close when the process exits).
	DrainTimeout time.Duration

	// PreDrainDelay is how long the server keeps serving after the stop
	// signal, with readiness already reporting draining, so orchestrators
	// observe unreadiness and stop routing before the listener closes.
	PreDrainDelay time.Duration
}

// Server wraps a net/http Server with signal-driven graceful shutdown and a
// draining-aware readiness probe. The zero value is not usable; construct
// with NewServer.
type Server struct {
	srv      *nethttp.Server
	opts     ServerOptions
	draining atomic.Bool
}

// NewServer wraps srv. The caller keeps configuring srv (handler, timeouts)
// as usual; only Serve/ListenAndServe must go through the wrapper.
func NewServer(srv *nethttp.Server, opts ServerOptions) *Server {
	return &Server{srv: srv, opts: opts}
}

// LivenessHandler serves the liveness probe. It checks nothing, on purpose:
// liveness answers "can this process still run a handler", and must never
// consult a dependency. A probe that fails on a Postgres blip restarts every
// replica of every service at once, turning a recoverable outage into a
// restart storm. Dependency health belongs on ReadinessHandler, which sheds
// traffic without a restart. Draining instances stay live deliberately:
// restarting a pod mid-drain discards its in-flight requests.
func LivenessHandler() nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(nethttp.StatusOK)
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			log.ForContext(r.Context()).Warn().Err(err).Msg("failed writing liveness response")
		}
	})
}

// ReadinessHandler serves the readiness probe: 503 as soon as shutdown
// begins (so routing stops before the listener closes), otherwise the
// aggregate of the component checks. Liveness probes must NOT use this —
// a draining instance is still alive; point liveness at LivenessHandler
// instead.
func (s *Server) ReadinessHandler(components ...HealthChecker) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if s.draining.Load() {
			JSONError(w, "draining", nethttp.StatusServiceUnavailable)
			return
		}

		// Bound the component checks so a wedged dependency produces a clean 503
		// instead of a probe timeout. Must stay below the probe's timeoutSeconds.
		checkCtx, cancel := context.WithTimeout(r.Context(), readinessCheckTimeout)
		defer cancel()

		for _, component := range components {
			if err := component.HealthCheck(checkCtx); err != nil {
				log.ForContext(r.Context()).Warn().Err(err).Msg("readiness check failed")
				JSONError(w, "readiness check failed", nethttp.StatusServiceUnavailable)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(nethttp.StatusOK)
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			log.ForContext(r.Context()).Warn().Err(err).Msg("failed writing readiness response")
		}
	})
}

// ListenAndServe binds the wrapped server's Addr and delegates to Serve.
func (s *Server) ListenAndServe(ctx context.Context) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.srv.Addr, err)
	}
	return s.Serve(ctx, listener)
}

// Serve serves on l until ctx is cancelled, then drains: readiness flips to
// 503 immediately, the server keeps accepting for PreDrainDelay, and
// Shutdown then closes the listener and waits up to DrainTimeout for
// in-flight requests. In-flight request contexts are never cancelled here —
// they derive from the server's BaseContext, which callers keep open through
// the drain.
//
// Returns nil after a clean drain or clean listener close; a non-nil error
// means the listener failed or the drain deadline expired. Either way the
// caller regains control so its deferred teardown runs.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	logger := log.ForContext(ctx)

	srvDone := make(chan error, 1)
	go func() {
		srvDone <- s.srv.Serve(l)
	}()

	logger.Info().Str("addr", l.Addr().String()).Msg("listening")

	select {
	case err := <-srvDone:
		if err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
			return fmt.Errorf("http server terminated unexpectedly: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	s.draining.Store(true)
	logger.Info().
		Dur("pre_drain_delay", s.opts.PreDrainDelay).
		Msg("draining: readiness now 503")

	if s.opts.PreDrainDelay > 0 {
		// Plain sleep, deliberately: the whole point of this window is to
		// keep serving while orchestrators notice unreadiness, so there is
		// nothing to wake up early for.
		time.Sleep(s.opts.PreDrainDelay)
	}

	logger.Info().
		Dur("drain_timeout", s.opts.DrainTimeout).
		Msg("shutting down http server")

	// Derive from ctx to keep its values, but drop its cancellation — ctx is
	// already done and Shutdown needs a live deadline to bound the drain.
	shutCtx, shutCancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.DrainTimeout)
	defer shutCancel()

	if err := s.srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("http server drain: %w", err)
	}

	// Serve returns ErrServerClosed once Shutdown begins — but if the
	// listener failed in the same instant the ctx was cancelled, this is a
	// real serve error and the drain was not clean.
	if err := <-srvDone; err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
		return fmt.Errorf("http server terminated during drain: %w", err)
	}
	logger.Info().Msg("http server drained")
	return nil
}
