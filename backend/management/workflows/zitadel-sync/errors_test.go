//nolint:testpackage // orgNotFoundErr is intentionally unexported; tests live alongside.
package zitadel_sync

import (
	"errors"
	"fmt"
	"testing"

	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
)

// TestOrgNotFoundErr verifies the classification used by
// FetchZitadelUsersActivity: a Zitadel NotFound (org deleted mid-cycle) or
// the SDK's org-probe sentinel becomes a non-retryable application error
// of type ErrZitadelOrgNotFound so TenantSyncWorkflow can skip the tenant;
// every other error is passed through unchanged (and stays retryable).
func TestOrgNotFoundErr(t *testing.T) {
	t.Parallel()

	gone := map[string]error{
		"gRPC NotFound":              status.Error(codes.NotFound, "Organisation not found (ORG-oL9nT)"),
		"wrapped org-probe sentinel": fmt.Errorf("%w: %q", sdk.ErrOrganizationNotFound, "org-1"),
	}
	for name, in := range gone {
		t.Run(name+" → non-retryable typed error", func(t *testing.T) {
			t.Parallel()

			out := orgNotFoundErr(in)

			var appErr *temporal.ApplicationError
			if !errors.As(out, &appErr) {
				t.Fatalf("got %T, want *temporal.ApplicationError", out)
			}
			if appErr.Type() != ErrZitadelOrgNotFound {
				t.Errorf("Type() = %q, want %q", appErr.Type(), ErrZitadelOrgNotFound)
			}
			if !appErr.NonRetryable() {
				t.Error("error should be non-retryable")
			}
		})
	}

	t.Run("other gRPC errors pass through unchanged", func(t *testing.T) {
		t.Parallel()

		in := status.Error(codes.Unavailable, "transient")

		out := orgNotFoundErr(in)

		if !errors.Is(out, in) {
			t.Fatalf("got %v, want the original error unchanged", out)
		}
		var appErr *temporal.ApplicationError
		if errors.As(out, &appErr) {
			t.Error("transient error must not be wrapped as a non-retryable application error")
		}
	})

	t.Run("non-gRPC errors pass through unchanged", func(t *testing.T) {
		t.Parallel()

		in := fmt.Errorf("plain error")

		if out := orgNotFoundErr(in); !errors.Is(out, in) {
			t.Fatalf("got %v, want the original error unchanged", out)
		}
	})
}
