package dataindex

// In-package: pointerSegments is unexported and is the shared rule the hook and
// the backfill both depend on.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPointerSegmentsRejectsPathsTheHookCannotFollow guards the invariant the
// whole design rests on: the SQL backfill and the Go hook must index the same
// key. An empty segment builds the malformed literal '{a,,b}', which fails the
// boot pass rather than the datatype save; an array index is followed by #> but
// not by resolvePointer, so the row would flip between indexed and unindexed
// depending on which path wrote last.
func TestPointerSegmentsRejectsPathsTheHookCannotFollow(t *testing.T) {
	t.Parallel()

	for name, pointer := range map[string]string{
		"empty segment":  "/a//b",
		"trailing slash": "/a/",
		"array index":    "/items/0",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := pointerSegments(pointer)
			require.ErrorIs(t, err, ErrBadSourcePath)
		})
	}
}
