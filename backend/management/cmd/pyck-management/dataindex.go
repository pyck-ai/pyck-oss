package main

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	entdatatype "github.com/pyck-ai/pyck/backend/management/ent/gen/datatype"
)

// errNoTenantForLineage means neither the mutation nor the request carries a
// tenant, so the lineage cannot be scoped. Failing closed keeps a nil-tenant
// query from returning an empty history that would reopen the rebind hole.
var errNoTenantForLineage = errors.New("no tenant to scope the datatype lineage")

// dataTypeLineageBindings returns the bindings every version of a (tenant, slug)
// has ever declared, unioned across all rows including soft-deleted ones.
//
// A slug is freed when its datatype is soft-deleted, so a recreated slug inherits
// the retired versions' slots; reading them here is what stops a rebind the
// entity rows -- which live in another service and still carry the old slug and
// slot values -- would otherwise silently disagree with.
func dataTypeLineageBindings(client *ent.Client) dataindex.LineageBindings {
	return func(ctx context.Context, tenantID uuid.UUID, slug string) (dataindex.Bindings, error) {
		if slug == "" {
			// Nothing to key a lineage on; the hook still checks the row itself.
			return dataindex.Bindings{}, nil
		}
		// On a create the tenant_id field is not set until a later mixin hook, so
		// fall back to the request tenant. If neither carries one, fail closed: a
		// nil-tenant query would return an empty history and let a recreated slug
		// rebind a slot unchecked. (HasMutationTenantID is checked first because
		// MutationTenantID panics rather than returning nil.)
		if tenantID == uuid.Nil {
			if req := request.ForContext(ctx); req.HasMutationTenantID() {
				tenantID = req.MutationTenantID()
			}
		}
		if tenantID == uuid.Nil {
			return nil, errNoTenantForLineage
		}
		// Oldest first (id is a time-ordered uuidv7): if two versions ever disagree
		// on a name, the earliest binding wins deterministically -- and it is the
		// one existing rows were indexed under, so it is also the correct one.
		rows, err := client.DataType.Query().
			Where(entdatatype.TenantID(tenantID), entdatatype.Slug(slug)).
			Order(entdatatype.ByID()).
			Select(entdatatype.FieldJSONSchema).
			Strings(feature.Context(ctx, feature.FEATURE_SHOW_DELETED))
		if err != nil {
			return nil, err
		}
		union := dataindex.Bindings{}
		for _, schema := range rows {
			bindings, err := dataindex.Parse(schema)
			if err != nil {
				return nil, err
			}
			for name, ix := range bindings {
				if _, seen := union[name]; !seen {
					union[name] = ix
				}
			}
		}
		return union, nil
	}
}
