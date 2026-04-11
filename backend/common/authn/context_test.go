package authn_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/stretchr/testify/assert"
)

func TestContext(t *testing.T) {
	tests := []struct {
		name string
		user *authn.User
	}{
		{
			name: "with normal user",
			user: &authn.User{
				ID:       uuid.New(),
				TenantID: uuid.New(),
				Username: "testuser",
				Roles: map[uuid.UUID]authn.Role{
					uuid.New(): authn.ROLE_ADMIN,
				},
			},
		},
		{
			name: "with system user",
			user: authn.SystemUser(),
		},
		{
			name: "with empty user",
			user: &authn.User{},
		},
		{
			name: "with nil user",
			user: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()

			// Add user to context
			ctxWithUser := authn.Context(ctx, tt.user)

			// Verify context is not nil
			assert.NotNil(t, ctxWithUser)

			// Verify the user can be retrieved
			if tt.user != nil {
				retrievedUser := authn.ForContext(ctxWithUser)
				assert.Equal(t, *tt.user, retrievedUser)
			}
		})
	}
}

func TestForContext(t *testing.T) {
	tests := []struct {
		name         string
		setupContext func() context.Context
		expectedUser authn.User
	}{
		{
			name: "retrieve existing user",
			setupContext: func() context.Context {
				user := &authn.User{
					ID:       uuid.New(),
					TenantID: uuid.New(),
					Username: "testuser",
				}
				return authn.Context(t.Context(), user)
			},
			expectedUser: func() authn.User {
				return authn.User{
					ID:       uuid.New(),
					TenantID: uuid.New(),
					Username: "testuser",
				}
			}(),
		},
		{
			name: "retrieve system user",
			setupContext: func() context.Context {
				return authn.Context(t.Context(), authn.SystemUser())
			},
			expectedUser: *authn.SystemUser(),
		},
		{
			name: "no user in context",
			setupContext: func() context.Context {
				return t.Context()
			},
			expectedUser: authn.User{},
		},
		{
			name: "nil user in context",
			setupContext: func() context.Context {
				return authn.Context(t.Context(), nil)
			},
			expectedUser: authn.User{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupContext()
			user := authn.ForContext(ctx)

			// For the "retrieve existing user" test, we need to check properties individually
			// since we can't predict the exact UUID values
			if tt.name == "retrieve existing user" {
				assert.NotEqual(t, uuid.Nil, user.ID)
				assert.NotEqual(t, uuid.Nil, user.TenantID)
				assert.Equal(t, "testuser", user.Username)
			} else {
				assert.Equal(t, tt.expectedUser, user)
			}
		})
	}
}

func TestContextIntegration(t *testing.T) {
	// Test full flow: create context, add user, retrieve user
	originalUser := &authn.User{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Username: "integrationuser",
		Roles: map[uuid.UUID]authn.Role{
			uuid.New(): authn.ROLE_READER,
		},
	}

	// Create context with user
	ctx := authn.Context(t.Context(), originalUser)

	// Retrieve user
	retrievedUser := authn.ForContext(ctx)

	// Verify retrieved user matches original
	assert.Equal(t, originalUser.ID, retrievedUser.ID)
	assert.Equal(t, originalUser.TenantID, retrievedUser.TenantID)
	assert.Equal(t, originalUser.Username, retrievedUser.Username)
	assert.Equal(t, originalUser.Roles, retrievedUser.Roles)
}

func TestContextChaining(t *testing.T) {
	// Test that context can be chained with other values
	type otherKey struct{}

	user := &authn.User{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Username: "chainuser",
	}

	// Create context with multiple values
	ctx := t.Context()
	ctx = context.WithValue(ctx, otherKey{}, "other value")
	ctx = authn.Context(ctx, user)

	// Verify both values are present
	retrievedUser := authn.ForContext(ctx)
	assert.Equal(t, user.Username, retrievedUser.Username)

	otherValue := ctx.Value(otherKey{}).(string)
	assert.Equal(t, "other value", otherValue)
}

func TestContextWithCancellation(t *testing.T) {
	// Test that user survives context cancellation
	user := &authn.User{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Username: "canceluser",
	}

	// Create cancellable context with user
	ctx, cancel := context.WithCancel(t.Context())
	ctx = authn.Context(ctx, user)

	// User should be retrievable before cancellation
	retrievedUser := authn.ForContext(ctx)
	assert.Equal(t, user.Username, retrievedUser.Username)

	// Cancel the context
	cancel()

	// User should still be retrievable after cancellation
	retrievedUser = authn.ForContext(ctx)
	assert.Equal(t, user.Username, retrievedUser.Username)
}

func BenchmarkContext(b *testing.B) {
	user := &authn.User{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Username: "benchuser",
	}
	ctx := b.Context()

	for b.Loop() {
		_ = authn.Context(ctx, user)
	}
}

func BenchmarkForContext(b *testing.B) {
	user := &authn.User{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Username: "benchuser",
	}
	ctx := authn.Context(b.Context(), user)

	for b.Loop() {
		_ = authn.ForContext(ctx)
	}
}
