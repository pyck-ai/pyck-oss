//go:build integration && !linux

package workersubscriptions_test

import "os/exec"

// dieWithParent is a no-op where Pdeathsig does not exist; workers then rely on
// TearDownTest to stop them.
func dieWithParent(*exec.Cmd) {}
