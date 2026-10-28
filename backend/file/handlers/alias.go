package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/std"

	ent "github.com/pyck-ai/pyck/backend/file/ent/gen"
	entfile "github.com/pyck-ai/pyck/backend/file/ent/gen/file"
	entprivacy "github.com/pyck-ai/pyck/backend/file/ent/gen/privacy"
	"github.com/pyck-ai/pyck/backend/file/services"
)

// FileAliasHandler returns an HTTP handler that redirects to a pre-signed S3 URL
// for files identified by tenant ID and alias. Uses 307 Temporary Redirect so that
// each request generates a fresh pre-signed URL, avoiding issues with URL expiration.
func FileAliasHandler(dbClient *ent.Client, s3Storage *services.S3StorageService, cache *liveness.Cache, source liveness.TenantSource) http.HandlerFunc {
	// One throttle for the handler: the fault it reports is the shared
	// liveness source being down, not a property of a single alias.
	faultLog := log.NewFaultThrottle(log.FaultLogWindow)
	return func(w http.ResponseWriter, r *http.Request) {
		tenantIDStr := chi.URLParam(r, "tenantId")
		alias := chi.URLParam(r, "alias")

		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			httputil.JSONError(w, "invalid tenant ID", http.StatusBadRequest)
			return
		}

		if !std.IsValidSlug(alias) {
			httputil.JSONError(w, "invalid public alias", http.StatusBadRequest)
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
				httputil.JSONError(w, "file not found", http.StatusNotFound)
				return
			}
			log.ForContext(r.Context()).Error().Err(err).
				Str("tenantID", tenantIDStr).
				Str("publicAlias", alias).
				Msg("Error querying file by public alias")
			httputil.JSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// The tenant itself must still be served: a soft-deleted or retired
		// tenant's public files must not stay reachable through old links.
		// Checked AFTER the file query on purpose: this endpoint is public
		// and unratelimited, so probing liveness first would let anyone
		// spray random tenant IDs into the verdict cache and its source;
		// only IDs that a real capability URL matches ever reach it. A dead
		// tenant answers exactly like a missing file, so the route stays
		// free of tenant-state oracles.
		live, lerr := cache.LookupTenants(r.Context(), []uuid.UUID{f.TenantID}, source)
		if lerr != nil {
			// Cached verdicts were applied; a tenant with no verdict is
			// denied below: a public link that cannot be verified is not
			// served during the fault, and a tenant this replica knows is
			// dead stays hidden.
			faultLog.Err(log.ForContext(r.Context()), lerr).
				Msg("tenant liveness lookup failed for public alias; denying uncached tenants")
		}
		if !live[f.TenantID] {
			httputil.JSONError(w, "file not found", http.StatusNotFound)
			return
		}

		presignedURL, err := s3Storage.GetPreSignedURL(f.TenantID, f.Refid, f.Name)
		if err != nil {
			log.ForContext(r.Context()).Error().Err(err).
				Str("fileID", f.ID.String()).
				Msg("Error generating pre-signed URL")
			httputil.JSONError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		http.Redirect(w, r, presignedURL, http.StatusTemporaryRedirect)
	}
}
