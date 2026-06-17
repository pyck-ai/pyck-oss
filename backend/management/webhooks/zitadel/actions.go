package zitadel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	zitadelactions "github.com/zitadel/zitadel-go/v3/pkg/actions"

	"github.com/pyck-ai/pyck/backend/common/authn"
	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// tenantIDClaim is the canonical name of the pyck tenant UUID claim emitted
// on OIDC tokens. Stays in lockstep with anyone reading the token (frontend,
// OpenBAO, third-party).
const tenantIDClaim = "pyck_tenant_id"

// preTokenRequest is the slice of the Actions v2 preuserinfo/preaccesstoken
// context payload that we care about. The Zitadel action runtime sends a
// rich body; we only read the user's resource_owner (org ID) and tolerate
// any other field shape changes Zitadel introduces.
type preTokenRequest struct {
	User struct {
		ID            string `json:"id"`
		ResourceOwner string `json:"resource_owner"`
	} `json:"user"`
	// Userinfo is a fallback: Zitadel mirrors the in-progress claim set here,
	// including urn:zitadel:iam:user:resourceowner:id when present.
	Userinfo map[string]any `json:"userinfo"`
}

// preTokenResponse is the Actions v2 contract for adding plain top-level
// claims to the outgoing OIDC token.
type preTokenResponse struct {
	AppendClaims []claimKV `json:"append_claims"`
}

type claimKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PreTokenHandler returns the HTTP handler that Zitadel's Actions v2
// preuserinfo + preaccesstoken executions POST to. The handler verifies the
// ZITADEL-Signature HMAC, extracts the user's resource_owner (Zitadel org
// ID) from the request payload, computes pyck_tenant_id =
// ComputeUUID(audience, resource_owner), and instructs Zitadel to splice it
// into the issued token as a plain top-level claim.
//
// The audience param MUST match what backend/common/authn uses to derive
// tenant UUIDs server-side (typically the Zitadel issuer URL). The
// signingKey is the shared HMAC secret provisioned alongside the Target;
// it MUST be non-empty — management's startup config refuses an empty
// value, and this handler verifies every request unconditionally. Any
// caller wiring in an empty key here is a programmer error and the
// handler will reject every request as unauthorized.
//
// Failure semantics — the webhook always returns 200 unless the request is
// malformed or unauthenticated, even when we can't compute a claim. That
// preserves token issuance: with `interrupt_on_error: false` on the Target,
// a failed webhook means tokens issue without our claim (which is exactly
// what happens today for any auth path we don't cover).
func PreTokenHandler(audience, signingKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := log.ForContext(r.Context())

		body, err := io.ReadAll(r.Body)
		if err != nil {
			httputil.JSONError(w, "read body", http.StatusBadRequest)
			return
		}

		if err := zitadelactions.ValidateRequestPayload(body, &r.Header, signingKey); err != nil {
			logger.Warn().Err(err).Msg("zitadel action signature rejected")
			httputil.JSONError(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var req preTokenRequest
		if err := json.Unmarshal(body, &req); err != nil {
			logger.Warn().Err(err).Msg("zitadel action payload not JSON")
			httputil.JSONError(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		orgID := extractResourceOwner(req)
		if orgID == "" {
			// No org context — nothing to compute. 200 + empty body so Zitadel
			// emits the token unchanged. Logged so we notice payload-shape
			// surprises on the Zitadel side.
			logger.Debug().Msg("zitadel action: no resource_owner in payload; emitting empty append_claims")
			writeJSON(r.Context(), w, preTokenResponse{})
			return
		}

		tenantID := authn.ComputeUUID(audience, orgID).String()
		writeJSON(r.Context(), w, preTokenResponse{
			AppendClaims: []claimKV{
				{Key: tenantIDClaim, Value: tenantID},
			},
		})
	}
}

// extractResourceOwner reads the user's home org from the action payload.
// Prefers user.resource_owner; falls back to the
// urn:zitadel:iam:user:resourceowner:id userinfo claim if Zitadel only
// populated the userinfo slot for this Execution kind.
func extractResourceOwner(req preTokenRequest) string {
	if req.User.ResourceOwner != "" {
		return req.User.ResourceOwner
	}
	if req.Userinfo != nil {
		if v, ok := req.Userinfo["urn:zitadel:iam:user:resourceowner:id"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func writeJSON(ctx context.Context, w http.ResponseWriter, body preTokenResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Body has already been (partially) written — the status + headers
		// went out the door before Encode. There's nothing useful left to
		// do beyond noting it, and Zitadel's RestCall path will see a
		// malformed JSON response and skip the claim. Logged so the
		// failure isn't invisible.
		log.ForContext(ctx).Warn().Err(err).Msg("zitadel action: failed to encode response body")
	}
}
