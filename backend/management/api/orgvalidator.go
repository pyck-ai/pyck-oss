package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/pyck-ai/pyck/backend/common/authn"
)

// ErrOrganizationInvalid is returned when the generated client reports
// no error but the response payload is missing the expected
// `organization` field. Indicates schema drift or gateway
// misconfiguration.
var ErrOrganizationInvalid = errors.New("management organization: empty response")

// NewOrganizationValidator returns an [authn.OrgValidator] backed by
// the generated management API client (calls the federated
// `organization(sub)` query through the gateway). Every backend
// service's auth middleware uses it as the post-introspection
// org-state probe; management itself uses an inline closure against
// the local gRPC connection instead.
//
// The supplied client MUST authenticate with the service's own
// PYCK_SERVICE_TOKEN — management's organization resolver rejects
// non-system callers.
func NewOrganizationValidator(client Client) authn.OrgValidator {
	return func(ctx context.Context, sub string) (bool, error) {
		out, err := client.GetOrganization(ctx, GetOrganizationArgs{Sub: sub})
		if err != nil {
			return false, fmt.Errorf("management organization: %w", err)
		}
		if out == nil {
			return false, ErrOrganizationInvalid
		}
		return out.Organization.Active, nil
	}
}
