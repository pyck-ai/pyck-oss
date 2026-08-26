package signals

import (
	"syscall"
	"testing"
)

// fakeSignal is an os.Signal that is not a syscall.Signal, exercising the
// exitCode fallback branch.
type fakeSignal struct{}

func (fakeSignal) String() string { return "fake" }
func (fakeSignal) Signal()        {}

func TestExitCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		sig  interface{ String() string }
		want int
	}{
		{"SIGINT", syscall.SIGINT, 130},
		{"SIGTERM", syscall.SIGTERM, 143},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sig, ok := tc.sig.(syscall.Signal)
			if !ok {
				t.Fatalf("test setup error: %v is not a syscall.Signal", tc.sig)
			}
			if got := exitCode(sig); got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", sig, got, tc.want)
			}
		})
	}

	if got := exitCode(fakeSignal{}); got != 1 {
		t.Errorf("exitCode(non-syscall.Signal) = %d, want 1", got)
	}
}
