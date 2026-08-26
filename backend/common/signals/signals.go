// Package signals wraps os/signal with a hard-exit escape hatch.
package signals

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// NotifyContext behaves like signal.NotifyContext: the returned context is
// cancelled by the first of sigs, and the returned func uninstalls the
// handler. It differs in one way — a SECOND signal, arriving while the
// graceful shutdown is still unwinding, exits the process immediately with
// 128+signum instead of being swallowed by the already-cancelled context.
//
// The uninstall is synchronous: once the returned func returns, sigs are no
// longer intercepted and revert to their default disposition (for SIGINT and
// SIGTERM: terminating the process).
//
// Orchestrators never send a second signal: Kubernetes and Docker both send
// SIGTERM once and then SIGKILL at the grace period. The second signal comes
// from a human at a terminal who has decided not to wait out the shutdown
// budget, so honouring it is unambiguously what was meant.
func NotifyContext(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	// Separate from ctx: ctx is already done after the first signal, so the
	// loop cannot use it to tell "shutting down" from "shutdown finished".
	stopped := make(chan struct{})
	var once sync.Once

	ch := make(chan os.Signal, 2)
	signal.Notify(ch, sigs...)

	// Uninstall the handler BEFORE waking the goroutine: once stop returns,
	// no further signal can reach ch, so nothing sent after stop can be
	// mistaken for the second-signal hard exit below.
	stop := func() {
		once.Do(func() {
			signal.Stop(ch)
			close(stopped)
		})
		cancel()
	}

	go func() {
		for {
			select {
			case sig := <-ch:
				// A signal buffered before stop uninstalled the handler can
				// still win this select over the closed stopped channel. The
				// caller has already detached, so it is not a second stop
				// signal — drop it instead of killing the process.
				select {
				case <-stopped:
					return
				default:
				}
				if ctx.Err() == nil {
					log.ForContext(parent).Info().
						Str("signal", sig.String()).
						Msg("stop signal received, draining — repeat the signal to exit immediately")
					cancel()
					continue
				}
				log.ForContext(parent).Warn().
					Str("signal", sig.String()).
					Msg("second stop signal received, exiting immediately; in-flight requests and the outbox batch are abandoned")
				os.Exit(exitCode(sig))
			case <-stopped:
				return
			}
		}
	}()

	return ctx, stop
}

// exitCode follows the shell convention: 130 for SIGINT, 143 for SIGTERM.
func exitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}
