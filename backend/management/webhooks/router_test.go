package webhooks_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/test/mocks"

	"github.com/pyck-ai/pyck/backend/management/webhooks"
)

// postStatus issues an unsigned POST to path against the router and returns the
// status code. An unsigned request to a mounted, signature-verified route is
// rejected with 401; an unmounted path yields 404 — so the status
// distinguishes "route mounted" from "route absent".
func postStatus(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestRouter_MountsLoginRouteWhenConfigured(t *testing.T) {
	t.Parallel()
	h := webhooks.Router(webhooks.Config{
		Publisher:                 &mocks.MockPublisher{},
		NatsStreamName:            "pyck",
		ZitadelLoginActionSignKey: "test-key",
	})

	if got := postStatus(t, h, "/zitadel/login"); got != http.StatusUnauthorized {
		t.Errorf("login route status = %d, want 401 (mounted, unsigned request rejected)", got)
	}
}

func TestRouter_OmitsLoginRouteWithoutSigningKey(t *testing.T) {
	t.Parallel()
	h := webhooks.Router(webhooks.Config{Publisher: &mocks.MockPublisher{}}) // no key

	if got := postStatus(t, h, "/zitadel/login"); got != http.StatusNotFound {
		t.Errorf("login route status = %d, want 404 (not mounted without key)", got)
	}
}

func TestRouter_OmitsLoginRouteWithoutPublisher(t *testing.T) {
	t.Parallel()
	h := webhooks.Router(webhooks.Config{ZitadelLoginActionSignKey: "test-key"}) // no publisher

	if got := postStatus(t, h, "/zitadel/login"); got != http.StatusNotFound {
		t.Errorf("login route status = %d, want 404 (not mounted without publisher)", got)
	}
}
