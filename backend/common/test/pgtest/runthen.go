package pgtest

import "go.uber.org/goleak"

// runThen is the [goleak.TestingM] returned by [RunThen].
type runThen struct {
	m     goleak.TestingM
	after func()
}

// RunThen wraps m so that its Run runs the tests and then after, returning
// the tests' exit code. Pass the result to [goleak.VerifyTestMain] with the
// container teardown (the terminate func from [Start], plus any tracer
// cleanup) as after.
//
// The teardown must happen inside Run: VerifyTestMain looks for leftover
// goroutines as soon as Run returns, so a container or tracer still running
// at that point is reported as a leak of the tests, and a deferred teardown
// in TestMain never runs because VerifyTestMain exits the process. after
// runs whatever the exit code, so a failing binary still removes its
// container.
func RunThen(m goleak.TestingM, after func()) goleak.TestingM {
	return runThen{m: m, after: after}
}

// Run runs the wrapped tests, then the after func, and returns the tests'
// exit code.
func (r runThen) Run() int {
	code := r.m.Run()
	r.after()
	return code
}
