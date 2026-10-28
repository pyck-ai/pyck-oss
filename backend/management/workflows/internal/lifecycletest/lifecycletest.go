// Package lifecycletest holds the fixtures the disable-tenant and
// restore-tenant activity tests share: an in-memory tenant table, worker-api
// stand-ins and Temporal error assertions.
package lifecycletest

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	_ "github.com/mattn/go-sqlite3" // enttest SQLite driver

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
)

// NewEntClient opens an in-memory SQLite Ent client closed with the test.
func NewEntClient(t *testing.T) *ent.Client {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return client
}

// CreateTenant inserts a tenant row with the given flavour data, soft-deleted
// when disabled is set.
func CreateTenant(t *testing.T, client *ent.Client, id uuid.UUID, data map[string]any, disabled bool) {
	t.Helper()

	q := client.Tenant.Create().
		SetID(id).
		SetName("tenant-" + id.String()).
		SetIdpOrgRef("org-" + id.String()).
		SetData(data)
	if disabled {
		q = q.SetDeletedAt(time.Now())
	}

	_, err := q.Save(authn.Context(t.Context(), authn.SystemUser()))
	require.NoError(t, err)
}

// PyckGoData is tenant data of the pyck-go flavour.
func PyckGoData() map[string]any { return map[string]any{"flavour": "pyck-go"} }

// JSONStatus answers every request with worker-api's JSON error shape.
func JSONStatus(code int, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":"` + msg + `"}`)) //nolint:errcheck // test server
	}
}

// RequireNonRetryable asserts err is a non-retryable Temporal application
// error of the given type.
func RequireNonRetryable(t *testing.T, err error, typ string) {
	t.Helper()

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, typ, appErr.Type())
	assert.True(t, appErr.NonRetryable())
}

// RequireRetryable asserts err is an error Temporal would retry.
func RequireRetryable(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)

	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		assert.False(t, appErr.NonRetryable())
	}
}
