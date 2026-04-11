package privacy_test

import (
	"testing"

	entprivacy "entgo.io/ent/privacy"
	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/authn/privacy"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlwaysAllowRule(t *testing.T) {
	rule := privacy.AlwaysAllowRule()
	require.NotNil(t, rule)

	ctx := t.Context()
	err := rule.EvalQuery(ctx, nil)
	assert.Equal(t, entprivacy.Allow, err)

	err = rule.EvalMutation(ctx, nil)
	assert.Equal(t, entprivacy.Allow, err)
}

func TestAlwaysDenyRule(t *testing.T) {
	rule := privacy.AlwaysDenyRule()
	require.NotNil(t, rule)

	ctx := t.Context()
	err := rule.EvalQuery(ctx, nil)
	assert.Equal(t, entprivacy.Deny, err)

	err = rule.EvalMutation(ctx, nil)
	assert.Equal(t, entprivacy.Deny, err)
}

func TestAllowIfRole(t *testing.T) {
	tenantID1 := uuid.New()
	tenantID2 := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name           string
		checkRole      authn.Role
		user           *authn.User
		tenantIDs      []uuid.UUID
		expectedResult error
	}{
		// READER role tests
		{
			name:      "reader role - user has exact role",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_READER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Allow,
		},
		{
			name:      "reader role - user has higher role (writer)",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_WRITER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Allow,
		},
		{
			name:      "reader role - user has higher role (admin)",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Allow,
		},
		{
			name:      "reader role - user has lower role",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_NONE,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		// WRITER role tests
		{
			name:      "writer role - user has exact role",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_WRITER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Allow,
		},
		{
			name:      "writer role - user has higher role (admin)",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Allow,
		},
		{
			name:      "writer role - user has lower role (reader)",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_READER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		// ADMIN role tests
		{
			name:      "admin role - user has exact role",
			checkRole: authn.ROLE_ADMIN,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Allow,
		},
		{
			name:      "admin role - user has lower role (writer)",
			checkRole: authn.ROLE_ADMIN,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_WRITER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:      "admin role - user has lower role (reader)",
			checkRole: authn.ROLE_ADMIN,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_READER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		// Cross-tenant tests
		{
			name:      "user has role in different tenant",
			checkRole: authn.ROLE_ADMIN,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID2: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:      "user lacks role in one of multiple queried tenants",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID2: authn.ROLE_WRITER,
					// Missing role for tenantID1
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1, tenantID2},
			expectedResult: entprivacy.Skip, // User doesn't have role in ALL tenants
		},
		{
			name:      "user has role in all queried tenants",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_WRITER,
					tenantID2: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1, tenantID2},
			expectedResult: entprivacy.Allow, // User has role in ALL tenants
		},
		// Unauthenticated user tests
		{
			name:      "unauthenticated user (nil IDs)",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       uuid.Nil,
				TenantID: uuid.Nil,
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:           "empty user",
			checkRole:      authn.ROLE_ADMIN,
			user:           &authn.User{},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			ctx = request.Context(ctx, tt.user, tt.tenantIDs...)

			rule := privacy.AllowIfRole(tt.checkRole)
			err := rule.EvalQuery(ctx, nil)
			assert.Equal(t, tt.expectedResult, err)

			err = rule.EvalMutation(ctx, nil)
			assert.Equal(t, tt.expectedResult, err)
		})
	}
}

func TestDenyIfNotRole(t *testing.T) {
	tenantID1 := uuid.New()
	tenantID2 := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name           string
		checkRole      authn.Role
		user           *authn.User
		tenantIDs      []uuid.UUID
		expectedResult error
	}{
		// READER role tests
		{
			name:      "reader role - user has exact role",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_READER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:      "reader role - user has higher role",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:      "reader role - user has lower role",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_NONE,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Deny,
		},
		// WRITER role tests
		{
			name:      "writer role - user has exact role",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_WRITER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:      "writer role - user has higher role",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:      "writer role - user has lower role",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_READER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Deny,
		},
		// ADMIN role tests
		{
			name:      "admin role - user has exact role",
			checkRole: authn.ROLE_ADMIN,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Skip,
		},
		{
			name:      "admin role - user has lower role",
			checkRole: authn.ROLE_ADMIN,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_WRITER,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Deny,
		},
		// Cross-tenant tests
		{
			name:      "user has role in different tenant",
			checkRole: authn.ROLE_ADMIN,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID2: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Deny,
		},
		{
			name:      "user lacks role in one of multiple queried tenants",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID2: authn.ROLE_WRITER,
					// Missing role for tenantID1
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1, tenantID2},
			expectedResult: entprivacy.Deny, // User doesn't have role in ALL tenants
		},
		{
			name:      "user has role in all queried tenants",
			checkRole: authn.ROLE_WRITER,
			user: &authn.User{
				ID:       userID,
				TenantID: tenantID1,
				Roles: map[uuid.UUID]authn.Role{
					tenantID1: authn.ROLE_WRITER,
					tenantID2: authn.ROLE_ADMIN,
				},
			},
			tenantIDs:      []uuid.UUID{tenantID1, tenantID2},
			expectedResult: entprivacy.Skip, // User has role in ALL tenants
		},
		// Unauthenticated user tests
		{
			name:      "unauthenticated user (nil IDs)",
			checkRole: authn.ROLE_READER,
			user: &authn.User{
				ID:       uuid.Nil,
				TenantID: uuid.Nil,
			},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Deny,
		},
		{
			name:           "empty user",
			checkRole:      authn.ROLE_ADMIN,
			user:           &authn.User{},
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Deny,
		},
		{
			name:           "nil user in context",
			checkRole:      authn.ROLE_WRITER,
			user:           nil,
			tenantIDs:      []uuid.UUID{tenantID1},
			expectedResult: entprivacy.Deny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			ctx = request.Context(ctx, tt.user, tt.tenantIDs...)

			rule := privacy.DenyIfNotRole(tt.checkRole)
			err := rule.EvalQuery(ctx, nil)
			assert.Equal(t, tt.expectedResult, err)

			err = rule.EvalMutation(ctx, nil)
			assert.Equal(t, tt.expectedResult, err)
		})
	}
}

func TestPublicWrapperFunctions(t *testing.T) {
	// Test that public wrapper functions use the correct underlying role
	tenantID := uuid.New()
	userID := uuid.New()

	t.Run("AllowIfReader uses ROLE_READER", func(t *testing.T) {
		user := &authn.User{
			ID:       userID,
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_READER,
			},
		}
		ctx := request.Context(t.Context(), user, tenantID)

		rule := privacy.AllowIfReader()
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Allow, err)
	})

	t.Run("DenyIfNoReader uses ROLE_READER", func(t *testing.T) {
		user := &authn.User{
			ID:       userID,
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_NONE,
			},
		}
		ctx := request.Context(t.Context(), user, tenantID)

		rule := privacy.DenyIfNoReader()
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Deny, err)
	})

	t.Run("AllowIfWriter uses ROLE_WRITER", func(t *testing.T) {
		user := &authn.User{
			ID:       userID,
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_WRITER,
			},
		}
		ctx := request.Context(t.Context(), user, tenantID)

		rule := privacy.AllowIfWriter()
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Allow, err)
	})

	t.Run("DenyIfNoWriter uses ROLE_WRITER", func(t *testing.T) {
		user := &authn.User{
			ID:       userID,
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_READER,
			},
		}
		ctx := request.Context(t.Context(), user, tenantID)

		rule := privacy.DenyIfNoWriter()
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Deny, err)
	})

	t.Run("AllowIfAdmin uses ROLE_ADMIN", func(t *testing.T) {
		user := &authn.User{
			ID:       userID,
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_ADMIN,
			},
		}
		ctx := request.Context(t.Context(), user, tenantID)

		rule := privacy.AllowIfAdmin()
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Allow, err)
	})

	t.Run("DenyIfNoAdmin uses ROLE_ADMIN", func(t *testing.T) {
		user := &authn.User{
			ID:       userID,
			TenantID: tenantID,
			Roles: map[uuid.UUID]authn.Role{
				tenantID: authn.ROLE_WRITER,
			},
		}
		ctx := request.Context(t.Context(), user, tenantID)

		rule := privacy.DenyIfNoAdmin()
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Deny, err)
	})
}

func TestMultipleTenantScenarios(t *testing.T) {
	tenantID1 := uuid.New()
	tenantID2 := uuid.New()
	tenantID3 := uuid.New()
	userID := uuid.New()

	t.Run("user with varying roles across tenants", func(t *testing.T) {
		user := &authn.User{
			ID:       userID,
			TenantID: tenantID1,
			Roles: map[uuid.UUID]authn.Role{
				tenantID1: authn.ROLE_READER,
				tenantID2: authn.ROLE_WRITER,
				tenantID3: authn.ROLE_ADMIN,
			},
		}

		// Query all tenants - requires role in ALL tenants
		ctx := request.Context(t.Context(), user, tenantID1, tenantID2, tenantID3)

		rule := privacy.AllowIfRole(authn.ROLE_READER)
		assert.Equal(t, entprivacy.Allow, rule.EvalQuery(ctx, nil)) // User has reader+ in all tenants

		rule = privacy.AllowIfRole(authn.ROLE_WRITER)
		assert.Equal(t, entprivacy.Skip, rule.EvalQuery(ctx, nil)) // User lacks writer in tenant1

		rule = privacy.AllowIfRole(authn.ROLE_ADMIN)
		assert.Equal(t, entprivacy.Skip, rule.EvalQuery(ctx, nil)) // User lacks admin in tenant1 and tenant2

		// Query only tenant where user is reader
		ctx = request.Context(t.Context(), user, tenantID1)

		rule = privacy.AllowIfRole(authn.ROLE_WRITER)
		assert.Equal(t, entprivacy.Skip, rule.EvalQuery(ctx, nil))

		rule = privacy.DenyIfNotRole(authn.ROLE_WRITER)
		assert.Equal(t, entprivacy.Deny, rule.EvalQuery(ctx, nil))
	})
}

func TestEmptyContext(t *testing.T) {
	// Test with completely empty context (no request context set)
	ctx := t.Context()

	t.Run("AllowIfRole with empty context", func(t *testing.T) {
		rule := privacy.AllowIfRole(authn.ROLE_ADMIN)
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Skip, err)
	})

	t.Run("privacy.DenyIfNotRole with empty context", func(t *testing.T) {
		rule := privacy.DenyIfNotRole(authn.ROLE_ADMIN)
		err := rule.EvalQuery(ctx, nil)
		assert.Equal(t, entprivacy.Deny, err)
	})
}
