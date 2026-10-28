package registertenant

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The lag predicates answer only for the two shapes a lagging read model
// produces; a refusal or a transport fault is not lag.
func TestProjectionLag(t *testing.T) {
	t.Parallel()

	for _, code := range []codes.Code{codes.NotFound, codes.FailedPrecondition} {
		assert.True(t, projectionLag(status.Error(code, "Errors.Org.NotFound")), code.String())
	}
	for _, code := range []codes.Code{codes.InvalidArgument, codes.AlreadyExists, codes.PermissionDenied, codes.Unavailable} {
		assert.False(t, projectionLag(status.Error(code, "dependency said "+code.String())), code.String())
	}
	assert.False(t, projectionLag(errors.New("no status at all")))
}

func TestNamespaceCacheLag(t *testing.T) {
	t.Parallel()

	assert.True(t, namespaceCacheLag(serviceerror.NewNamespaceNotFound("ns")))
	// Temporal's namespace errors carry Status(), not GRPCStatus(); a plain
	// gRPC NotFound is Zitadel's shape and is projectionLag's business.
	assert.False(t, namespaceCacheLag(status.Error(codes.NotFound, "not a namespace error")))
	assert.False(t, namespaceCacheLag(serviceerror.NewUnavailable("restarting")))
}

// After this run wrote the object, the lag answers come back unchanged so
// the retry policy retries them; everything else is classified exactly as
// commonwf.Classify would.
func TestClassifyAfterWrite(t *testing.T) {
	t.Parallel()

	nonRetryable := func(err error) bool {
		info := temporal.GetDefaultFailureConverter().ErrorToFailure(err).GetApplicationFailureInfo()
		return info != nil && info.GetNonRetryable()
	}

	t.Run("read-model lag stays retryable", func(t *testing.T) {
		t.Parallel()
		for name, in := range map[string]error{
			"zitadel not found":           status.Error(codes.NotFound, "Errors.Org.NotFound"),
			"zitadel failed precondition": status.Error(codes.FailedPrecondition, "Errors.Org.NotFound"),
			"temporal namespace":          serviceerror.NewNamespaceNotFound("ns"),
		} {
			got := classifyAfterWrite(t.Context(), appErrTypeOrgMetadata, in)
			require.Equal(t, in, got, "%s: must come back unchanged", name)
			assert.False(t, nonRetryable(got), name)
		}
	})

	t.Run("a refusal is still permanent", func(t *testing.T) {
		t.Parallel()
		for _, code := range []codes.Code{codes.InvalidArgument, codes.AlreadyExists, codes.PermissionDenied} {
			in := status.Error(code, "dependency said "+code.String())
			got := classifyAfterWrite(t.Context(), appErrTypeOrgMetadata, in)
			require.True(t, nonRetryable(got), code.String())
			require.ErrorIs(t, got, in)
		}
	})

	t.Run("a transport fault is still retryable", func(t *testing.T) {
		t.Parallel()
		in := status.Error(codes.Unavailable, "zitadel is restarting")
		assert.Equal(t, in, classifyAfterWrite(t.Context(), appErrTypeOrgMetadata, in))
	})

	t.Run("nil is nil", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, classifyAfterWrite(t.Context(), appErrTypeOrgMetadata, nil))
	})
}
