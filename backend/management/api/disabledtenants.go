package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/liveness"
)

// ErrDisabledTenantsInvalid is returned when the generated client reports
// no error but the `disabledTenants` payload is missing or holds an ID
// that is not a UUID. Indicates schema drift or gateway misconfiguration.
var ErrDisabledTenantsInvalid = errors.New("management disabledTenants: invalid response")

// NewDisabledTenantsSource returns a [liveness.TenantSource] backed by
// management's system-only `disabledTenants` query through the generated
// client. The supplied client must authenticate with the service's
// PYCK_SERVICE_TOKEN — the resolver rejects non-system callers.
//
// Every returned ID is one of the requested IDs by contract; an ID that
// does not parse as a UUID means schema drift or a gateway decode fault
// and is surfaced as an error (unknown state, not a verdict) rather than
// silently dropped — dropping it would turn "management said disabled"
// into "allowed".
func NewDisabledTenantsSource(client Client) liveness.TenantSource {
	return func(ctx context.Context, tenantIDs []uuid.UUID) ([]uuid.UUID, error) {
		if len(tenantIDs) == 0 {
			return nil, nil
		}
		ids := make([]string, len(tenantIDs))
		for i, id := range tenantIDs {
			ids[i] = id.String()
		}
		resp, err := client.GetDisabledTenants(ctx, GetDisabledTenantsArgs{Ids: ids})
		if err != nil {
			return nil, fmt.Errorf("management disabledTenants: %w", err)
		}
		if resp == nil {
			return nil, ErrDisabledTenantsInvalid
		}
		disabled := make([]uuid.UUID, 0, len(resp.DisabledTenants))
		for _, raw := range resp.DisabledTenants {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrDisabledTenantsInvalid, raw, err)
			}
			disabled = append(disabled, id)
		}
		return disabled, nil
	}
}
