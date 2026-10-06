//go:build integration && linux

package workersubscriptions_test

import (
	"os/exec"
	"syscall"
)

// dieWithParent makes the kernel SIGKILL the child when the test binary exits
// (including a panic or a kill), so a crashed run cannot leave workers
// heartbeating against a leaked tenant. Pdeathsig fires when the parent
// *thread* that forked the child exits; the Go runtime keeps its threads alive
// unless a goroutine locked to one exits, which this suite never does.
func dieWithParent(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
