package resolvers

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
)

// writerTenantID returns the request's tenant if the caller is a WRITER in it.
// Temporal-only mutations need it: ent privacy checks roles only on DB writes.
func writerTenantID(req request.RequestContext) (uuid.UUID, error) {
	tenantID, err := singleTenantID(req)
	if err != nil {
		return uuid.Nil, err
	}
	if !req.User().HasRole(authn.ROLE_WRITER, tenantID) {
		return uuid.Nil, ErrWriterRoleRequired
	}
	return tenantID, nil
}
