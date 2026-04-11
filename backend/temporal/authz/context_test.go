package authz_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/temporal/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/server/common/authorization"
)

func TestGetClaims(t *testing.T) {
	testCases := []struct {
		name           string
		ctxFunc        func() context.Context
		expectedClaims *authorization.Claims
	}{
		{
			name:           "NoClaimsInContext",
			ctxFunc:        func() context.Context { return t.Context() },
			expectedClaims: nil,
		},
		{
			name: "ClaimsInContext",
			ctxFunc: func() context.Context {
				return context.WithValue(t.Context(), authorization.MappedClaims, &authorization.Claims{
					Subject: "test-subject",
				})
			},
			expectedClaims: &authorization.Claims{
				Subject: "test-subject",
			},
		},
		{
			name: "WrongTypeInContext",
			ctxFunc: func() context.Context {
				return context.WithValue(t.Context(), authorization.MappedClaims, "not-claims")
			},
			expectedClaims: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			claims := authz.GetClaims(tc.ctxFunc())
			assert.Equal(t, tc.expectedClaims, claims)
		})
	}
}

func TestGetClaimExtensions(t *testing.T) {
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")

	testCases := []struct {
		name        string
		claims      *authorization.Claims
		expectedExt *authz.ClaimMapperExtensions
	}{
		{
			name:        "NilClaims",
			claims:      nil,
			expectedExt: nil,
		},
		{
			name: "NoExtensions",
			claims: &authorization.Claims{
				Subject: "test-subject",
			},
			expectedExt: nil,
		},
		{
			name: "WrongExtensionType",
			claims: &authorization.Claims{
				Subject:    "test-subject",
				Extensions: "not-extensions",
			},
			expectedExt: nil,
		},
		{
			name: "ValidExtensions",
			claims: &authorization.Claims{
				Subject: "test-subject",
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       userID,
						TenantID: tenantID,
						Username: "test-user",
					},
				},
			},
			expectedExt: &authz.ClaimMapperExtensions{
				User: authn.User{
					ID:       userID,
					TenantID: tenantID,
					Username: "test-user",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ext := authz.GetClaimExtensions(tc.claims)
			assert.Equal(t, tc.expectedExt, ext)
		})
	}
}

func TestGetUser(t *testing.T) {
	userID := uuid.MustParse("da3185b5-fc6a-4dbe-81d5-f5f43c1847e8")
	tenantID := uuid.MustParse("64d4ce68-3abc-420f-9397-bbabfe45f313")

	testCases := []struct {
		name         string
		claims       *authorization.Claims
		expectedUser authn.User
	}{
		{
			name:         "NilClaims",
			claims:       nil,
			expectedUser: authn.User{},
		},
		{
			name: "NoExtensions",
			claims: &authorization.Claims{
				Subject: "test-subject",
			},
			expectedUser: authn.User{},
		},
		{
			name: "ValidUser",
			claims: &authorization.Claims{
				Subject: "test-subject",
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       userID,
						TenantID: tenantID,
						Username: "test-user",
						Roles: map[uuid.UUID]authn.Role{
							tenantID: authn.ROLE_ADMIN,
						},
					},
				},
			},
			expectedUser: authn.User{
				ID:       userID,
				TenantID: tenantID,
				Username: "test-user",
				Roles: map[uuid.UUID]authn.Role{
					tenantID: authn.ROLE_ADMIN,
				},
			},
		},
		{
			name: "SystemUser",
			claims: &authorization.Claims{
				Subject: uuid.Max.String(),
				Extensions: &authz.ClaimMapperExtensions{
					User: authn.User{
						ID:       uuid.Max,
						TenantID: uuid.Max,
						Username: "system",
					},
				},
			},
			expectedUser: authn.User{
				ID:       uuid.Max,
				TenantID: uuid.Max,
				Username: "system",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			user := authz.GetUser(tc.claims)
			assert.Equal(t, tc.expectedUser, user)

			if tc.name == "SystemUser" {
				require.True(t, user.IsSystemUser())
			}
		})
	}
}
