// Package startup provides the stop-signal checkpoint used between the
// uncancellable phases of service startup. Threaded cancellation is not
// available for every dependency (e.g. Temporal and NATS clients accept a
// context but do not honor it for the connect call), so mains poll the
// stop-signal context explicitly between startup steps instead.
package startup

import (
	"context"
	"errors"
	"fmt"
)

// ErrAborted marks a startup that was cut short by a stop signal rather than
// by a failure. Callers unwrap it in main to exit 0 and log at info: SIGTERM
// during startup is a normal request to stop, not a crash.
var ErrAborted = errors.New("startup aborted by stop signal")

// Check returns a wrapped ErrAborted if ctx is already cancelled, naming the
// step that had just completed. Place calls between startup steps, always
// AFTER the defer that releases the resource the preceding step created, so
// the abort path still tears it down.
func Check(ctx context.Context, step string) error {
	if ctx.Err() == nil {
		return nil
	}
	return fmt.Errorf("%w: after %s", ErrAborted, step)
}
