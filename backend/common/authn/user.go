package authn

import (
	"github.com/google/uuid"
)

func SystemUser() *User {
	return &User{
		ID:       uuid.Max,
		TenantID: uuid.Max,
		Username: "system",
	}
}

type User struct {
	// ID is the unique pyck-side identifier of the user
	// (deterministic UUID hashed from issuer + sub).
	ID uuid.UUID
	// TenantID is the unique identifier of the tenant the user belongs to.
	TenantID uuid.UUID
	// Sub is the raw Zitadel user ID — the `sub` claim from the token's
	// introspection, and the value ID above is derived from. Kept verbatim
	// as the diagnostic identifier of the auth path: a log line naming it
	// can be matched against Zitadel's own user records, which ID (hashed)
	// cannot.
	Sub string
	// Username is the unique name of the user.
	Username string
	// Roles is a map of roles the user has in each tenant.
	Roles map[uuid.UUID]Role
	// ServiceRoles holds the per-service gate role keys (e.g.
	// "inventory_service", see package serviceroles) the user has in each
	// tenant. Tracked separately from the privilege ladder; the gate enforces
	// them per service.
	ServiceRoles map[uuid.UUID]map[string]struct{}
	// Token is the authentication token of the user, the caller's live
	// bearer credential. It MUST never reach a log line or an event
	// payload: log sites name explicit identifier fields (ID, Sub,
	// Username) instead of handing the whole struct to the logger. The
	// json:"-" tag makes that structural: the struct is marshalled into
	// event payloads and diagnostic dumps, and a tag is the only thing
	// that keeps a future call site from carrying the credential there.
	Token string `json:"-"`
}

func (u User) IsAuthenticated() bool {
	return u.ID != uuid.Nil && u.TenantID != uuid.Nil
}

func (u User) IsSystemUser() bool {
	return u.ID == uuid.Max && u.TenantID == uuid.Max
}

func (u User) TenantIDs() []uuid.UUID {
	tenantIDs := make([]uuid.UUID, 0, len(u.Roles))
	for tenantID := range u.Roles {
		tenantIDs = append(tenantIDs, tenantID)
	}

	return tenantIDs
}

func (u User) Role(tenantIDs ...uuid.UUID) Role {
	if !u.IsAuthenticated() {
		return ROLE_NONE
	}

	role := ROLE_NONE

	for i, tenantID := range tenantIDs {
		if i == 0 {
			role = u.Roles[tenantID]
		} else if r, ok := u.Roles[tenantID]; ok && r < role {
			role = r
		}
	}

	return role
}

// HasServiceRole reports whether the user holds the given per-service gate
// role key in the tenant. System users implicitly hold every service role.
func (u User) HasServiceRole(key string, tenantID uuid.UUID) bool {
	if u.IsSystemUser() {
		return true
	}

	roles, ok := u.ServiceRoles[tenantID]
	if !ok {
		return false
	}

	_, ok = roles[key]
	return ok
}

// HasRole checks if the user has a specific role in a given tenant.
// It returns true if the users has the specified role in ALL of the specified tenants.
func (u User) HasRole(role Role, tenantIDs ...uuid.UUID) bool {
	if !u.IsAuthenticated() {
		return false
	}

	if u.IsSystemUser() {
		return true
	}

	if len(tenantIDs) == 0 {
		return false
	}

	for _, tenantID := range tenantIDs {
		if u.Role(tenantID) < role {
			return false
		}
	}

	return true
}
