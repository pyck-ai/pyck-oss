package resolver

import (
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
)

// Common test tenant IDs used across all services.
var (
	TenantA = uuid.MustParse("b98b88eb-ce77-4e9a-a224-d37443a9c5c1")
	TenantB = uuid.MustParse("9820ed57-8fca-40e0-958b-f4f428774cde")
)

// Common test users used across all services.
var (
	UserA = &authn.User{
		ID:       uuid.MustParse("fdd880fd-c97e-4b8a-83fa-653b1960d87b"),
		TenantID: TenantA,
		Roles:    map[uuid.UUID]authn.Role{TenantA: authn.ROLE_ADMIN},
	}
	UserB = &authn.User{
		ID:       uuid.MustParse("a5e5bcf2-e3de-4b37-ac5d-08808ae464ba"),
		TenantID: TenantB,
		Roles:    map[uuid.UUID]authn.Role{TenantB: authn.ROLE_WRITER},
	}
)
