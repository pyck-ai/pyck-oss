package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/pyck-ai/pyck/backend/common/std"

	ent "github.com/pyck-ai/pyck/backend/file/ent/gen"
	entfile "github.com/pyck-ai/pyck/backend/file/ent/gen/file"
	entprivacy "github.com/pyck-ai/pyck/backend/file/ent/gen/privacy"
	"github.com/pyck-ai/pyck/backend/file/services"
)

// FileAliasHandler returns an HTTP handler that redirects to a pre-signed S3 URL
// for files identified by tenant ID and alias. Uses 307 Temporary Redirect so that
// each request generates a fresh pre-signed URL, avoiding issues with URL expiration.
func FileAliasHandler(dbClient *ent.Client, s3Storage *services.S3StorageService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantIDStr := chi.URLParam(r, "tenantId")
		alias := chi.URLParam(r, "alias")

		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			http.Error(w, "invalid tenant ID", http.StatusBadRequest)
			return
		}

		if !std.IsValidSlug(alias) {
			http.Error(w, "invalid public alias", http.StatusBadRequest)
			return
		}

		// Bypass privacy rules since this endpoint does its own tenant isolation
		ctx := entprivacy.DecisionContext(r.Context(), entprivacy.Allow)

		f, err := dbClient.File.Query().
			Where(
				entfile.TenantID(tenantID),
				entfile.PublicAlias(alias),
				entfile.DeletedAtIsNil(),
			).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				http.Error(w, "file not found", http.StatusNotFound)
				return
			}
			log.Error().Err(err).
				Str("tenantID", tenantIDStr).
				Str("publicAlias", alias).
				Msg("Error querying file by public alias")
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		presignedURL, err := s3Storage.GetPreSignedURL(f.TenantID, f.Refid, f.Name)
		if err != nil {
			log.Error().Err(err).
				Str("fileID", f.ID.String()).
				Msg("Error generating pre-signed URL")
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		http.Redirect(w, r, presignedURL, http.StatusTemporaryRedirect)
	}
}
