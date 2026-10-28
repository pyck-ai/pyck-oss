package registertenant

import (
	"context"
	"errors"
	"fmt"

	objectv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	orgv2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// errOrganizationIDMismatch reports that Zitadel created the organization
// under a different ID than the one requested. Zitadel releases before
// v4.10.0 ignore AddOrganizationRequest.OrganizationId and mint their own;
// a tenant created that way would not match the ID its tenant row was
// derived from.
var errOrganizationIDMismatch = errors.New("zitadel ignored the requested organization id")

// errOrganizationNameTaken reports that the requested name belongs to an
// organization other than the requested ID. Organization names are unique
// per instance, so no retry frees the name.
var errOrganizationNameTaken = errors.New("organization name is taken by another organization")

// errOrganizationOwnerUnresolved reports that Zitadel refused the creation
// as AlreadyExists but neither the requested ID nor the requested name
// resolves to an organization. The refusal comes from the eventstore and
// both lookups read the organization projection, so this is the window in
// which a creation the caller already made is not queryable yet. Retrying
// resolves it into one of the two settled answers.
var errOrganizationOwnerUnresolved = errors.New("organization owner unresolved")

// errOrganizationNotFound is returned by organizationByID and
// organizationIDByName when no live organization matches: absent, or still
// projected as REMOVED.
var errOrganizationNotFound = errors.New("organization not found")

// addOrganizationWithID creates an organization under a caller-chosen ID.
// Idempotent by ID: when Zitadel answers AlreadyExists and an organization
// with that ID exists, that is this registration's own earlier attempt and
// the call succeeds. The other outcomes of an AlreadyExists are
// errOrganizationNameTaken and errOrganizationOwnerUnresolved; see
// resolveOrganizationConflict.
func addOrganizationWithID(ctx context.Context, orgs orgv2.OrganizationServiceClient, name, orgID string) error {
	resp, err := orgs.AddOrganization(ctx, &orgv2.AddOrganizationRequest{
		Name:           name,
		OrganizationId: &orgID,
	})
	if status.Code(err) == codes.AlreadyExists {
		return resolveOrganizationConflict(ctx, orgs, name, orgID)
	}
	if err != nil {
		return err
	}
	if got := resp.GetOrganizationId(); got != orgID {
		err := fmt.Errorf("%w: requested %q, got %q", errOrganizationIDMismatch, orgID, got)
		// The org now exists under an ID no tenant row references; left in
		// place it would hold the name against every later registration.
		if _, delErr := orgs.DeleteOrganization(ctx, &orgv2.DeleteOrganizationRequest{OrganizationId: got}); delErr != nil {
			return errors.Join(err, fmt.Errorf("delete organization %q: %w", got, delErr))
		}
		return err
	}

	return nil
}

// resolveOrganizationConflict decides what an AlreadyExists from
// AddOrganization means. The ID and the name are both unique per instance and
// either can be the one that collided, so the answer is one of three: the
// organization under the requested ID is the caller's own earlier attempt,
// the name belongs to a different organization, or neither is visible yet.
//
// The refusal comes from the eventstore, which is immediately consistent,
// while both lookups read the organization projection, which is not. A miss
// is therefore not evidence that nothing exists, which is why the third case
// is an error of its own instead of a refusal: reporting a taken name here
// would permanently fail a registration whose organization was created a
// moment ago.
func resolveOrganizationConflict(ctx context.Context, orgs orgv2.OrganizationServiceClient, name, orgID string) error {
	// The caller mints the ID, so an organization carrying it can only be
	// this registration's own earlier attempt.
	switch err := organizationByID(ctx, orgs, orgID); {
	case err == nil:
		return nil
	case !errors.Is(err, errOrganizationNotFound):
		return err
	}

	owner, err := organizationIDByName(ctx, orgs, name)
	switch {
	case errors.Is(err, errOrganizationNotFound):
		return fmt.Errorf("%w: neither organization %q nor name %q is projected yet", errOrganizationOwnerUnresolved, orgID, name)
	case err != nil:
		return err
	case owner == orgID:
		// The projection caught up between the two lookups.
		return nil
	default:
		// The holding organization is for operators only, logged here and
		// never put in the error: registerTenant is a public sign-up
		// endpoint and this string travels back to the caller verbatim. A
		// tenant ID is ComputeUUID(audience, idp_org_ref) over an audience
		// anyone can read, so naming the organization hands out the holding
		// tenant's ID and turns a refused registration into an
		// instance-wide name-to-tenant oracle. The caller learns the name
		// it must change, which is all it can act on. resolveTakenUsername
		// withholds its holder for the same reason.
		log.ForContext(ctx).Info().
			Str("organization_name", name).
			Str("holder_org", owner).
			Str("requested_org", orgID).
			Msg("registration organization name is held by another organization")
		return fmt.Errorf("%w: name %q", errOrganizationNameTaken, name)
	}
}

// organizationByID answers whether a live organization exists under orgID:
// nil when it does, errOrganizationNotFound when it is absent or projected
// as REMOVED, and the wrapped transport failure otherwise. Unlike
// sdk.GetOrganizationByID (the original this mirrors, which stays in the
// SDK for zitadel-sync until that workflow retires with the one-way sync)
// it runs on the caller's client and reports existence only, because the
// conflict probe needs no fields of the organization.
func organizationByID(ctx context.Context, orgs orgv2.OrganizationServiceClient, orgID string) error {
	resp, err := orgs.ListOrganizations(ctx, &orgv2.ListOrganizationsRequest{
		Queries: []*orgv2.SearchQuery{{
			Query: &orgv2.SearchQuery_IdQuery{IdQuery: &orgv2.OrganizationIDQuery{Id: orgID}},
		}},
	})
	if err != nil {
		return fmt.Errorf("lookup organization %q: %w", orgID, err)
	}
	for _, org := range resp.GetResult() {
		if org.GetId() == orgID && org.GetState() != orgv2.OrganizationState_ORGANIZATION_STATE_REMOVED {
			return nil
		}
	}

	return fmt.Errorf("%w: %q", errOrganizationNotFound, orgID)
}

// organizationIDByName resolves an organization name to the ID of the live
// organization carrying it. Zitadel enforces name uniqueness ignoring case,
// so the query matches on the same terms. errOrganizationNotFound means no
// live organization carries the name.
func organizationIDByName(ctx context.Context, orgs orgv2.OrganizationServiceClient, name string) (string, error) {
	resp, err := orgs.ListOrganizations(ctx, &orgv2.ListOrganizationsRequest{
		Queries: []*orgv2.SearchQuery{{
			Query: &orgv2.SearchQuery_NameQuery{NameQuery: &orgv2.OrganizationNameQuery{
				Name:   name,
				Method: objectv2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE,
			}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("lookup organization by name %q: %w", name, err)
	}
	for _, org := range resp.GetResult() {
		if org.GetState() != orgv2.OrganizationState_ORGANIZATION_STATE_REMOVED {
			return org.GetId(), nil
		}
	}

	return "", fmt.Errorf("%w: name %q", errOrganizationNotFound, name)
}
