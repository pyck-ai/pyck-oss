// Package workerproc builds and runs helper worker binaries as OS processes
// for suites that need real workflowsdk workers against the live stack.
//
// One process serves one set of workflows, because the SDK reads its
// workflows from a package-level registry: to get several "worker types" (or
// several replicas of one) a suite starts several processes. A process exits
// the SDK's graceful way on SIGTERM (unregister its subscriptions, then stop
// the Temporal workers), which Stop relies on.
package workerproc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrExitedEarly marks an error about a process that died on its own.
var ErrExitedEarly = errors.New("process is gone")

// ErrStopTimeout marks a process that ignored SIGTERM and had to be killed.
var ErrStopTimeout = errors.New("worker did not exit after SIGTERM")

// Build compiles the main package pkg into dir and returns the binary's path.
// Build it once per suite run.
func Build(pkg, dir string) (string, error) {
	bin := filepath.Join(dir, filepath.Base(pkg))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, pkg).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build %s: %w\n%s", pkg, err, out)
	}

	return bin, nil
}

// Proc is one running worker process, its output going to a log file.
type Proc struct {
	Label string

	cmd     *exec.Cmd
	logPath string
	done    chan struct{}
	// waitErr is the result of cmd.Wait, valid once done is closed.
	waitErr error
	// stopping is set once Stop was called, so an exit after that is not
	// reported as a crash.
	stopping atomic.Bool
}

// Start launches bin with exactly env as its environment (nothing is
// inherited, so nothing from the shell can leak into the worker's
// configuration) and logs its output to logPath. The process is not tied to
// ctx's cancellation: it only ends through Stop, or when ctx ends it as a
// backstop against a leaked process.
func Start(ctx context.Context, bin string, env []string, logPath, label string) (*Proc, error) {
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("create worker log: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = env
	dieWithParent(cmd)

	if err := cmd.Start(); err != nil {
		_ = logFile.Close() //nolint:errcheck // log file only; the start error is what matters
		return nil, fmt.Errorf("start worker %s: %w", label, err)
	}

	p := &Proc{Label: label, cmd: cmd, logPath: logPath, done: make(chan struct{})}

	go func() {
		p.waitErr = cmd.Wait()
		_ = logFile.Close() //nolint:errcheck // log file only
		close(p.done)
	}()

	return p, nil
}

// Pid is the process ID.
func (p *Proc) Pid() int { return p.cmd.Process.Pid }

// LogPath is where the process output goes.
func (p *Proc) LogPath() string { return p.logPath }

// Exited reports whether the process has ended.
func (p *Proc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Stopping reports whether Stop was called, so an exit is expected.
func (p *Proc) Stopping() bool { return p.stopping.Load() }

// CrashedError returns an error describing an unexpected exit, or nil while
// the process is running or was asked to stop. It fails a wait fast when a
// worker died on its own.
func (p *Proc) CrashedError() error {
	if p.Stopping() || !p.Exited() {
		return nil
	}

	return fmt.Errorf("worker %s exited unexpectedly (wait: %w)\n%s", p.Label, errors.Join(p.waitErr, ErrExitedEarly), p.LogTail(30))
}

// LogTail returns the last n lines of the process output.
func (p *Proc) LogTail(n int) string {
	raw, err := os.ReadFile(p.logPath)
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", p.logPath, err)
	}

	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n")
}

// LogContains reports whether the process output holds s.
func (p *Proc) LogContains(s string) (bool, error) {
	raw, err := os.ReadFile(p.logPath)
	if err != nil {
		return false, err
	}

	return strings.Contains(string(raw), s), nil
}

// Stop sends SIGTERM (the SDK's graceful path) and waits up to timeout for
// the process to exit, then kills it. It returns the process's exit error.
func (p *Proc) Stop(timeout time.Duration) error {
	p.stopping.Store(true)

	if p.Exited() {
		return p.waitErr
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal worker %s: %w", p.Label, err)
	}

	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill() //nolint:errcheck // already failing
		<-p.done

		return fmt.Errorf("%w: %s killed after %s", ErrStopTimeout, p.Label, timeout)
	}
}
