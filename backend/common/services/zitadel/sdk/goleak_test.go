package sdk_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package's test binary when a goroutine started by any
// of its tests is still running once they all returned. The ignore list is
// empty on purpose: a goroutine this package's code leaves behind (an
// httptest server or a gRPC connection a client never closed) is a
// finding, not noise to filter.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
