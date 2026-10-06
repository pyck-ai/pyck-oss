//go:build !linux

package workerproc

import "os/exec"

// dieWithParent is a no-op where Pdeathsig does not exist; workers then rely on
// the suites' teardown to stop them.
func dieWithParent(*exec.Cmd) {}
