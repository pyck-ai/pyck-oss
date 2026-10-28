package pgtest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/test/pgtest"
)

// fakeM is a goleak.TestingM whose Run returns a fixed exit code.
type fakeM struct {
	code int
	ran  *bool
}

func (f fakeM) Run() int {
	*f.ran = true
	return f.code
}

func TestRunThen_RunsAfterTheTestsWhateverTheExitCode(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 1} {
		var ran, afterRan, afterSawRun bool
		wrapped := pgtest.RunThen(fakeM{code: code, ran: &ran}, func() {
			afterRan = true
			afterSawRun = ran
		})

		assert.Equal(t, code, wrapped.Run(), "exit code %d must pass through", code)
		assert.True(t, afterRan, "after must run for exit code %d", code)
		assert.True(t, afterSawRun, "after must run once the tests returned (exit code %d)", code)
	}
}
