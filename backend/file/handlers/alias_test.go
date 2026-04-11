package handlers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"

	ent "github.com/pyck-ai/pyck/backend/file/ent/gen"
	"github.com/pyck-ai/pyck/backend/file/ent/gen/enttest"
	entprivacy "github.com/pyck-ai/pyck/backend/file/ent/gen/privacy"
	"github.com/pyck-ai/pyck/backend/file/handlers"
	"github.com/pyck-ai/pyck/backend/file/services"
)

func setupHandler(t *testing.T) (*chi.Mux, *ent.Client) {
	t.Helper()

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	).Debug()

	s3Storage := &services.S3StorageService{
		Bucket:       "test-bucket",
		HTTPScheme:   "http",
		HTTPEndpoint: "localhost:9000",
		MinioClient:  mocks.NewMockMinioClient(),
	}

	r := chi.NewRouter()
	r.Get("/api/v1/files/{tenantId}/{alias}", handlers.FileAliasHandler(client, s3Storage))

	return r, client
}

func testUser(tenantID uuid.UUID) *authn.User {
	return &authn.User{
		ID:       uuidgql.GenerateV7UUID(),
		TenantID: tenantID,
		Roles: map[uuid.UUID]authn.Role{
			tenantID: authn.ROLE_ADMIN,
		},
	}
}

func createTestFile(t *testing.T, client *ent.Client, tenantID uuid.UUID, name, alias string) *ent.File {
	t.Helper()
	user := testUser(tenantID)
	ctx := request.Context(t.Context(), user, tenantID)
	ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)
	return client.File.Create().
		SetTenantID(tenantID).
		SetRefid(uuidgql.GenerateV7UUID()).
		SetReftype("supplier").
		SetName(name).
		SetSize(1024).
		SetContentType("image/png").
		SetPublicAlias(alias).
		SaveX(ctx)
}

func TestFileAliasHandler(t *testing.T) {
	t.Parallel()

	tenantID := resolver.TenantA

	t.Run("redirects to pre-signed URL", func(t *testing.T) {
		t.Parallel()
		router, client := setupHandler(t)

		createTestFile(t, client, tenantID, "logo.png", "company-logo")

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/api/v1/files/%s/company-logo", tenantID), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
		location := w.Header().Get("Location")
		require.NotEmpty(t, location)
		assert.Contains(t, location, "localhost:9000")

		// Verify no-cache headers
		assert.Equal(t, "no-cache, no-store, must-revalidate", w.Header().Get("Cache-Control"))
		assert.Equal(t, "no-cache", w.Header().Get("Pragma"))
		assert.Equal(t, "0", w.Header().Get("Expires"))
	})

	t.Run("returns 404 for non-existent alias", func(t *testing.T) {
		t.Parallel()
		router, _ := setupHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/api/v1/files/%s/non-existent", tenantID), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for invalid tenant ID", func(t *testing.T) {
		t.Parallel()
		router, _ := setupHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/files/not-a-uuid/some-alias", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 400 for invalid alias format", func(t *testing.T) {
		t.Parallel()
		router, _ := setupHandler(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/api/v1/files/%s/INVALID%%20ALIAS!", tenantID), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("isolates files by tenant", func(t *testing.T) {
		t.Parallel()
		router, client := setupHandler(t)

		tenantB := resolver.TenantB
		createTestFile(t, client, tenantB, "logo-b.png", "tenant-logo")

		// Request with tenant A should not find tenant B's file
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/api/v1/files/%s/tenant-logo", tenantID), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("does not return soft-deleted files", func(t *testing.T) {
		t.Parallel()
		router, client := setupHandler(t)

		f := createTestFile(t, client, tenantID, "deleted.png", "deleted-file")

		// Soft-delete the file
		user := testUser(tenantID)
		ctx := request.Context(t.Context(), user, tenantID)
		ctx = entprivacy.DecisionContext(ctx, entprivacy.Allow)
		client.File.UpdateOneID(f.ID).
			SetDeletedAt(time.Now()).
			SetDeletedBy(user.ID).
			SaveX(ctx)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/api/v1/files/%s/deleted-file", tenantID), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
