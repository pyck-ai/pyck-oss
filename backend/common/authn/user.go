package authn

import "github.com/google/uuid"

func SystemUser() *User {
	return &User{
		ID:       uuid.Max,
		TenantID: uuid.Max,
		Username: "system",
	}
}

type User struct {
	// ID is the unique identifier of the user.
	ID uuid.UUID
	// TenantID is the unique identifier of the tenant the user belongs to.
	TenantID uuid.UUID
	// Username is the unique name of the user.
	Username string
	// Roles is a map of roles the user has in each tenant.
	Roles map[uuid.UUID]Role
	// Token is the authentication token of the user.
	Token string
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
