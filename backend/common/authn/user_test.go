package authn_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/stretchr/testify/assert"
)

func TestSystemUser(t *testing.T) {
	t.Parallel()

	user := authn.SystemUser()

	assert.NotNil(t, user, "SystemUser should not return nil")
	assert.Equal(t, uuid.Max, user.ID, "System user ID should be uuid.Max")
	assert.Equal(t, uuid.Max, user.TenantID, "System user TenantID should be uuid.Max")
	assert.Equal(t, "system", user.Username, "System user should have username 'system'")
	assert.Nil(t, user.Roles, "System user should have nil roles")
}

func TestUser_IsAuthenticated(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		user     authn.User
		expected bool
	}{
		{
			name: "authenticated user with valid IDs",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: uuid.New(),
			},
			expected: true,
		},
		{
			name: "unauthenticated user with nil ID",
			user: authn.User{
				ID:       uuid.Nil,
				TenantID: uuid.New(),
			},
			expected: false,
		},
		{
			name: "unauthenticated user with nil TenantID",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: uuid.Nil,
			},
			expected: false,
		},
		{
			name: "unauthenticated user with both nil",
			user: authn.User{
				ID:       uuid.Nil,
				TenantID: uuid.Nil,
			},
			expected: false,
		},
		{
			name:     "system user is authenticated",
			user:     *authn.SystemUser(),
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, tc.user.IsAuthenticated())
		})
	}
}

func TestUser_IsSystemUser(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		user     authn.User
		expected bool
	}{
		{
			name:     "system user",
			user:     *authn.SystemUser(),
			expected: true,
		},
		{
			name: "regular user",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: uuid.New(),
			},
			expected: false,
		},
		{
			name: "user with only ID as Max",
			user: authn.User{
				ID:       uuid.Max,
				TenantID: uuid.New(),
			},
			expected: false,
		},
		{
			name: "user with only TenantID as Max",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: uuid.Max,
			},
			expected: false,
		},
		{
			name: "nil user",
			user: authn.User{
				ID:       uuid.Nil,
				TenantID: uuid.Nil,
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, tc.user.IsSystemUser())
		})
	}
}

func TestUser_TenantIDs(t *testing.T) {
	t.Parallel()

	tenant1 := uuid.New()
	tenant2 := uuid.New()
	tenant3 := uuid.New()

	testCases := []struct {
		name     string
		user     authn.User
		expected []uuid.UUID
	}{
		{
			name: "user with multiple tenants",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
					tenant2: authn.ROLE_WRITER,
					tenant3: authn.ROLE_READER,
				},
			},
			expected: []uuid.UUID{tenant1, tenant2, tenant3},
		},
		{
			name: "user with single tenant",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			expected: []uuid.UUID{tenant1},
		},
		{
			name: "user with no roles",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles:    map[uuid.UUID]authn.Role{},
			},
			expected: []uuid.UUID{},
		},
		{
			name: "user with nil roles",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles:    nil,
			},
			expected: []uuid.UUID{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := tc.user.TenantIDs()
			assert.Len(t, result, len(tc.expected), "should return correct number of tenant IDs")

			// Create a map for easier comparison (order doesn't matter)
			resultMap := make(map[uuid.UUID]bool)
			for _, id := range result {
				resultMap[id] = true
			}

			for _, expectedID := range tc.expected {
				assert.True(t, resultMap[expectedID], "should contain expected tenant ID")
			}
		})
	}
}

func TestUser_Role(t *testing.T) {
	t.Parallel()

	tenant1 := uuid.New()
	tenant2 := uuid.New()
	tenant3 := uuid.New()

	testCases := []struct {
		name      string
		user      authn.User
		tenantIDs []uuid.UUID
		expected  authn.Role
	}{
		{
			name: "single tenant with admin role",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs: []uuid.UUID{tenant1},
			expected:  authn.ROLE_ADMIN,
		},
		{
			name: "multiple tenants returns lowest role",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
					tenant2: authn.ROLE_WRITER,
					tenant3: authn.ROLE_READER,
				},
			},
			tenantIDs: []uuid.UUID{tenant1, tenant2, tenant3},
			expected:  authn.ROLE_READER,
		},
		{
			name: "tenant not in roles returns ROLE_NONE",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs: []uuid.UUID{tenant2},
			expected:  authn.ROLE_NONE,
		},
		{
			name: "unauthenticated user returns ROLE_NONE",
			user: authn.User{
				ID:       uuid.Nil,
				TenantID: uuid.Nil,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs: []uuid.UUID{tenant1},
			expected:  authn.ROLE_NONE,
		},
		{
			name: "no tenant IDs provided returns ROLE_NONE",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs: []uuid.UUID{},
			expected:  authn.ROLE_NONE,
		},
		{
			name: "mixed present and absent tenants",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
					tenant2: authn.ROLE_WRITER,
				},
			},
			tenantIDs: []uuid.UUID{tenant1, tenant3},
			expected:  authn.ROLE_ADMIN,
		},
		{
			name: "second tenant has lower role",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
					tenant2: authn.ROLE_READER,
				},
			},
			tenantIDs: []uuid.UUID{tenant1, tenant2},
			expected:  authn.ROLE_READER,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, tc.user.Role(tc.tenantIDs...))
		})
	}
}

func TestUser_HasRole(t *testing.T) {
	t.Parallel()

	tenant1 := uuid.New()
	tenant2 := uuid.New()
	tenant3 := uuid.New()

	testCases := []struct {
		name      string
		user      authn.User
		role      authn.Role
		tenantIDs []uuid.UUID
		expected  bool
	}{
		{
			name: "user has exact role in single tenant",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			role:      authn.ROLE_ADMIN,
			tenantIDs: []uuid.UUID{tenant1},
			expected:  true,
		},
		{
			name: "user has higher role than required",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			role:      authn.ROLE_WRITER,
			tenantIDs: []uuid.UUID{tenant1},
			expected:  true,
		},
		{
			name: "user has lower role than required",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_READER,
				},
			},
			role:      authn.ROLE_ADMIN,
			tenantIDs: []uuid.UUID{tenant1},
			expected:  false,
		},
		{
			name: "user must have role in all specified tenants",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
					tenant2: authn.ROLE_ADMIN,
					tenant3: authn.ROLE_READER,
				},
			},
			role:      authn.ROLE_WRITER,
			tenantIDs: []uuid.UUID{tenant1, tenant2, tenant3},
			expected:  false,
		},
		{
			name: "user has sufficient role in all specified tenants",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
					tenant2: authn.ROLE_WRITER,
					tenant3: authn.ROLE_WRITER,
				},
			},
			role:      authn.ROLE_WRITER,
			tenantIDs: []uuid.UUID{tenant1, tenant2, tenant3},
			expected:  true,
		},
		{
			name: "unauthenticated user returns false",
			user: authn.User{
				ID:       uuid.Nil,
				TenantID: uuid.Nil,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			role:      authn.ROLE_READER,
			tenantIDs: []uuid.UUID{tenant1},
			expected:  false,
		},
		{
			name:      "system user always returns true",
			user:      *authn.SystemUser(),
			role:      authn.ROLE_ADMIN,
			tenantIDs: []uuid.UUID{tenant1, tenant2},
			expected:  true,
		},
		{
			name: "no tenant IDs provided returns false",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			role:      authn.ROLE_READER,
			tenantIDs: []uuid.UUID{},
			expected:  false,
		},
		{
			name: "user missing role in one tenant",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			role:      authn.ROLE_READER,
			tenantIDs: []uuid.UUID{tenant1, tenant2},
			expected:  false,
		},
		{
			name: "checking for ROLE_NONE",
			user: authn.User{
				ID:       uuid.New(),
				TenantID: tenant1,
				Roles: map[uuid.UUID]authn.Role{
					tenant1: authn.ROLE_ADMIN,
				},
			},
			role:      authn.ROLE_NONE,
			tenantIDs: []uuid.UUID{tenant1},
			expected:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, tc.user.HasRole(tc.role, tc.tenantIDs...))
		})
	}
}

func TestUser_Username(t *testing.T) {
	t.Parallel()

	user := authn.User{
		Username: "testuser@example.com",
	}

	assert.Equal(t, "testuser@example.com", user.Username, "Username should be accessible")
}

func TestUser_HasServiceRole(t *testing.T) {
	t.Parallel()

	tenantA := uuid.New()
	tenantB := uuid.New()

	user := authn.User{
		ID:       uuid.New(),
		TenantID: tenantA,
		ServiceRoles: map[uuid.UUID]map[string]struct{}{
			tenantA: {"inventory_service": struct{}{}},
		},
	}

	testCases := []struct {
		name     string
		user     authn.User
		key      string
		tenantID uuid.UUID
		expected bool
	}{
		{"held in tenant", user, "inventory_service", tenantA, true},
		{"not held in tenant", user, "picking_service", tenantA, false},
		{"no roles in other tenant", user, "inventory_service", tenantB, false},
		{"system user holds every service role", *authn.SystemUser(), "inventory_service", tenantA, true},
		{"nil service roles", authn.User{ID: uuid.New(), TenantID: tenantA}, "inventory_service", tenantA, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, tc.user.HasServiceRole(tc.key, tc.tenantID))
		})
	}
}
