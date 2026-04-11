package mixin_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	ent "github.com/pyck-ai/pyck/backend/common/test/ent/gen"
	"github.com/pyck-ai/pyck/backend/common/test/ent/gen/enttest"
)

var (
	userID1 = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	userID2 = uuid.MustParse("22222222-2222-2222-2222-222222222222")

	tenantID1 = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tenantID2 = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	tenantID3 = uuid.MustParse("33333333-3333-3333-3333-333333333333")
)

func requestContext(t *testing.T, role authn.Role, userID uuid.UUID, tenantID uuid.UUID, TenantIDs ...uuid.UUID) context.Context {
	t.Helper()

	user := &authn.User{
		ID: userID,
		Roles: map[uuid.UUID]authn.Role{
			tenantID: role,
		},
		TenantID: tenantID,
	}

	for _, queryTenantID := range TenantIDs {
		user.Roles[queryTenantID] = role
	}

	ctx := t.Context()
	ctx = authn.Context(ctx, user)
	ctx = tenant.Context(ctx, append([]uuid.UUID{tenantID}, TenantIDs...)...)

	return ctx
}

func systemContext(t *testing.T, tenantIDs ...uuid.UUID) context.Context {
	t.Helper()

	user := authn.SystemUser()

	ctx := t.Context()
	ctx = authn.Context(ctx, user)
	ctx = tenant.Context(ctx, tenantIDs...)

	return ctx
}

func newDBClient(t *testing.T) *ent.Client {
	t.Helper()

	opts := []enttest.Option{
		enttest.WithOptions(ent.Log(t.Log), ent.Debug()),
	}
	return enttest.Open(t, dialect.SQLite, fmt.Sprintf("file:ent-data-%s-%d?mode=memory&_fk=1", t.Name(), time.Now().UnixNano()), opts...)
}
