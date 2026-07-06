package resolvers

import "errors"

// ErrOrganizationForbidden is returned when a non-system caller invokes
// the organization query. The query is the auth-path probe every
// backend service makes after introspection; opening it to non-system
// callers would let any authenticated user fish for arbitrary users'
// org state.
var ErrOrganizationForbidden = errors.New("organization: forbidden — system caller required")

// ErrNoUITemplateChange is returned when setTenantUITemplate is called with no
// field set: an all-no-op request is a malformed caller, not a silent success.
var ErrNoUITemplateChange = errors.New("setTenantUITemplate: request must set or clear at least one template")

// ErrConflictingUITemplateChange is returned when setTenantUITemplate is asked to
// both set and clear the same platform's template in one call.
var ErrConflictingUITemplateChange = errors.New("setTenantUITemplate: cannot set and clear the same template")

// Service-role assignment errors.
var (
	// ErrTenantNotFound is returned when the target tenant does not exist
	// or has been soft-deleted.
	ErrTenantNotFound = errors.New("tenant not found")
	// ErrTenantNoOrgRef is returned when the tenant has no Zitadel
	// organization reference to grant roles against.
	ErrTenantNoOrgRef = errors.New("tenant has no organization reference")
	// ErrUserNotFoundInTenant is returned when the target user does not
	// exist within the given tenant.
	ErrUserNotFoundInTenant = errors.New("user not found in tenant")
	// ErrNoRolesProvided is returned when an assign request carries no roles.
	ErrNoRolesProvided = errors.New("at least one role is required")
	// ErrNotAServiceRole is returned when a requested role key is not an
	// assignable per-service role (e.g. a ladder role).
	ErrNotAServiceRole = errors.New("only per-service roles may be assigned")
	// ErrTenantNoProjectGrant is returned when the tenant org has no project
	// grant for the central Pyck project to extend.
	ErrTenantNoProjectGrant = errors.New("tenant has no project grant")
)
