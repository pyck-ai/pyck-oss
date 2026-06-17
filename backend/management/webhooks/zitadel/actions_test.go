package zitadel_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	zitadelactions "github.com/zitadel/zitadel-go/v3/pkg/actions"

	"github.com/pyck-ai/pyck/backend/common/authn"

	zitadelwh "github.com/pyck-ai/pyck/backend/management/webhooks/zitadel"
)

const (
	testAudience  = "http://localhost:8080"
	testOrgID     = "373604903056048133"
	testSignKey   = "test-signing-key-32-bytes-of-junk"
	tenantIDClaim = "pyck_tenant_id" // mirrors the const in actions.go; pinned here so the wire contract is asserted at the test boundary
	webhookPath   = "/webhook/zitadel/actions/pre-token"
)

// claimsResponse mirrors actions.go's preTokenResponse for assertion via the
// wire format — kept as a plain struct here so the test exercises exactly
// what Zitadel will deserialize.
type claimsResponse struct {
	AppendClaims []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"append_claims"`
}

// signedReq returns a request signed with the SDK's ZITADEL-Signature helper
// so the handler's signature check accepts it. Using the real helper keeps
// the test in lockstep with whatever HMAC layout the SDK uses.
func signedReq(t *testing.T, body []byte, key string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, webhookPath, bytes.NewReader(body))
	req.Header.Set(zitadelactions.SigningHeader, zitadelactions.ComputeSignatureHeader(time.Now(), body, key))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeClaims(t *testing.T, body io.Reader) claimsResponse {
	t.Helper()
	var resp claimsResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestPreTokenHandler_EmitsClaimOnValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"user":{"id":"user-1","resource_owner":"` + testOrgID + `"}}`)

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeClaims(t, rec.Body)
	if len(resp.AppendClaims) != 1 {
		t.Fatalf("got %d append_claims, want 1: %+v", len(resp.AppendClaims), resp.AppendClaims)
	}
	got := resp.AppendClaims[0]
	wantValue := authn.ComputeUUID(testAudience, testOrgID).String()
	if got.Key != tenantIDClaim {
		t.Errorf("claim key = %q, want %q", got.Key, tenantIDClaim)
	}
	if got.Value != wantValue {
		t.Errorf("claim value = %q, want %q (ComputeUUID(%q, %q))", got.Value, wantValue, testAudience, testOrgID)
	}
}

func TestPreTokenHandler_UsesUserinfoFallback(t *testing.T) {
	t.Parallel()
	body := []byte(`{"userinfo":{"urn:zitadel:iam:user:resourceowner:id":"` + testOrgID + `"}}`)

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeClaims(t, rec.Body)
	want := authn.ComputeUUID(testAudience, testOrgID).String()
	if len(resp.AppendClaims) != 1 || resp.AppendClaims[0].Value != want {
		t.Fatalf("expected fallback to userinfo claim, got %+v", resp.AppendClaims)
	}
}

func TestPreTokenHandler_PrefersUserResourceOwnerOverUserinfo(t *testing.T) {
	t.Parallel()
	// Both set; user.resource_owner must win — userinfo is the fallback only.
	preferred := testOrgID
	other := "999999999999999999"
	body := []byte(`{"user":{"resource_owner":"` + preferred + `"},"userinfo":{"urn:zitadel:iam:user:resourceowner:id":"` + other + `"}}`)

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeClaims(t, rec.Body)
	want := authn.ComputeUUID(testAudience, preferred).String()
	if len(resp.AppendClaims) != 1 || resp.AppendClaims[0].Value != want {
		t.Fatalf("expected claim from user.resource_owner (%s), got %+v", preferred, resp.AppendClaims)
	}
}

func TestPreTokenHandler_IgnoresNonStringUserinfoClaim(t *testing.T) {
	t.Parallel()
	// Defensive: if Zitadel ever ships the userinfo field as a non-string
	// type, the handler must NOT panic and must NOT emit a claim.
	body := []byte(`{"userinfo":{"urn:zitadel:iam:user:resourceowner:id":42}}`)

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeClaims(t, rec.Body)
	if len(resp.AppendClaims) != 0 {
		t.Fatalf("expected empty append_claims when userinfo claim is non-string, got %+v", resp.AppendClaims)
	}
}

func TestPreTokenHandler_NoResourceOwnerYieldsEmptyClaims(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no resource_owner → empty claims, not error)", rec.Code)
	}
	resp := decodeClaims(t, rec.Body)
	if len(resp.AppendClaims) != 0 {
		t.Fatalf("expected empty append_claims when resource_owner missing, got %+v", resp.AppendClaims)
	}
}

func TestPreTokenHandler_RejectsBadSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"user":{"resource_owner":"` + testOrgID + `"}}`)

	// Signed with a DIFFERENT key than the handler trusts.
	req := signedReq(t, body, "wrong-key")

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPreTokenHandler_RejectsMissingSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"user":{"resource_owner":"` + testOrgID + `"}}`)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, webhookPath, bytes.NewReader(body))
	// no ZITADEL-Signature header

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when signature header is missing", rec.Code)
	}
}

func TestPreTokenHandler_RejectsWhenSigningKeyEmpty(t *testing.T) {
	t.Parallel()
	// Defense in depth: management's startup config refuses an empty
	// PYCK_ZITADEL_ACTION_SIGNING_KEY, so this branch is unreachable in
	// production. If somehow constructed with an empty key, the handler
	// MUST NOT silently accept unsigned traffic — every signature check
	// runs unconditionally, and signing with "" can't match a legitimate
	// Zitadel signature, so the request is rejected as 401.
	body := []byte(`{"user":{"resource_owner":"` + testOrgID + `"}}`)

	req := signedReq(t, body, testSignKey) // valid signature against testSignKey

	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, "")(rec, req) // handler key is empty

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — empty signing key must NEVER accept requests", rec.Code)
	}
}

func TestPreTokenHandler_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	body := []byte(`{not json`)
	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, signedReq(t, body, testSignKey))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPreTokenHandler_BodyReadErrorReturns400(t *testing.T) {
	t.Parallel()
	// Sanity: io.ReadAll returning an error short-circuits to 400 without
	// touching the signature path.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, webhookPath, errReader{})
	rec := httptest.NewRecorder()
	zitadelwh.PreTokenHandler(testAudience, testSignKey)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on body read error", rec.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
