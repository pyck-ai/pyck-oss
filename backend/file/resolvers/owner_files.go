package resolvers

import (
	"context"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"

	ent "github.com/pyck-ai/pyck/backend/file/ent/gen"
	entfile "github.com/pyck-ai/pyck/backend/file/ent/gen/file"
)

// ownerFiles lists the files that name the entity id as their refid, in the
// entity's own tenant. The tenant condition is explicit because refid is not
// a foreign key: a file of another tenant can name the entity, and neither the
// system user (who skips the tenant filter) nor a multi-tenant reader (whose
// filter spans every tenant it acts in) would exclude it.
func (r *entityResolver) ownerFiles(ctx context.Context, id, tenantID uuid.UUID) ([]*ent.File, error) {
	return r.client.File.Query().Where(entfile.Refid(id), entfile.TenantID(tenantID)).AllPages(ctx, mixin.Limit)
}
