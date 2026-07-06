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
// the generated management API client. The supplied client must
// authenticate with the service's PYCK_SERVICE_TOKEN — management's
// organization resolver rejects non-system callers.
//
// A nil response or a zero-valued Organization edge (nil ID) returns
// ErrOrganizationInvalid rather than (Active=false, nil): the Active
// bool alone can't distinguish "explicitly inactive" from "decode
// produced an empty struct".
func NewOrganizationValidator(client Client) authn.OrgValidator {
	return func(ctx context.Context, sub string) (bool, error) {
		out, err := client.GetOrganization(ctx, GetOrganizationArgs{Sub: sub})
		if err != nil {
			return false, fmt.Errorf("management organization: %w", err)
		}
		if out == nil || out.Organization.ID == nil {
			return false, ErrOrganizationInvalid
		}
		return out.Organization.Active, nil
	}
}
