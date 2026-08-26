package signals_test

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/pyck-ai/pyck/backend/common/signals"
)

// These tests deliberately do NOT run in parallel. signal.Notify is
// process-wide, so a signal one test sends is delivered to every handler
// another has installed — and a second SIGUSR1 reaching a live handler is
// exactly the os.Exit path, which would kill the test binary mid-run.
func TestNotifyContextCancelledByFirstSignal(t *testing.T) {
	ctx, stop := signals.NotifyContext(context.Background(), syscall.SIGUSR1)
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("failed to send signal: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context was not cancelled by first signal")
	}
}

func TestStopIsSafeToCallMoreThanOnce(t *testing.T) {
	_, stop := signals.NotifyContext(context.Background(), syscall.SIGUSR2)

	stop()
	stop()
	stop()
}

// TestStopMakesLateSignalHarmless hammers the stop-then-signal ordering: a
// signal arriving after stop() has returned must be ignored, never taken for
// the "second stop signal" that hard-exits the process — that would kill this
// test binary with exit 138. A single iteration only catches the wrong
// ordering about half the time, so it loops until the failure is a
// near-certainty.
//
//nolint:paralleltest // signal delivery is process-wide, see file comment
func TestStopMakesLateSignalHarmless(t *testing.T) {
	// Once no channel at all subscribes to SIGUSR1 its disposition reverts
	// to the OS default — terminate. The probe keeps a handler installed so
	// the kills below can only crash through the bug under test, and
	// receiving on it confirms each signal really was delivered before the
	// next iteration re-registers.
	probe := make(chan os.Signal, 1)
	signal.Notify(probe, syscall.SIGUSR1)
	defer signal.Stop(probe)

	for range 200 {
		_, stop := signals.NotifyContext(context.Background(), syscall.SIGUSR1)
		stop()

		if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
			t.Fatalf("failed to send signal: %v", err)
		}

		select {
		case <-probe:
		case <-time.After(2 * time.Second):
			t.Fatal("signal was not delivered to the probe handler")
		}
	}
}

func TestStopUninstallsHandlerAndGoroutineExits(t *testing.T) {
	// With NotifyContext detached, SIGUSR1 would revert to its default
	// disposition — terminate — and the Kill below would take down the test
	// binary. The probe keeps a handler installed so the signal is observable
	// instead of fatal.
	probe := make(chan os.Signal, 1)
	signal.Notify(probe, syscall.SIGUSR1)
	defer signal.Stop(probe)

	ctx, stop := signals.NotifyContext(context.Background(), syscall.SIGUSR1)
	stop()

	// After stop, the handler is uninstalled: sending the signal must not
	// trigger the second-signal hard exit, and must not crash the process.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("failed to send signal: %v", err)
	}

	select {
	case <-ctx.Done():
		// Expected: stop() cancels ctx itself.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stop() did not cancel its own context")
	}
}
