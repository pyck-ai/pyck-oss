package zitadel_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"

	zitadelwh "github.com/pyck-ai/pyck/backend/management/webhooks/zitadel"
)

const (
	testStreamName   = "pyck"
	testCustomTopic  = "pyck.custom-events"
	testUserAggID    = "373604903056099999"
	testEventType    = "user.human.password.check.succeeded"
	testEventCreated = "2026-06-01T08:25:31.397163Z"
)

// loginEventBody builds a minimal Actions v2 Event-execution payload for a
// login event. Mirrors the verbatim Zitadel payload shape (aggregateID =
// user, resourceOwner = org) so the test asserts the real wire contract.
func loginEventBody(aggregateID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"aggregateID":   aggregateID,
		"aggregateType": "user",
		"resourceOwner": testOrgID,
		"event_type":    testEventType,
		"created_at":    testEventCreated,
	})
	return b
}

// publishedMsg mirrors events.CustomEventMessage on the wire (no json tags →
// Go field names; UUIDs marshal as strings). Pinned here so the consumer
// contract is asserted at the test boundary.
type publishedMsg struct {
	Type      string `json:"Type"`
	Operation string `json:"Operation"`
	UserID    string `json:"UserID"`
	TenantID  string `json:"TenantID"`
	DataID    string `json:"DataID"`
}

func TestLoginHandler_PublishesLoginEventOnValidSignature(t *testing.T) {
	t.Parallel()
	body := loginEventBody(testUserAggID)

	wantUser := authn.ComputeUUID(testAudience, testUserAggID).String()
	wantTenant := authn.ComputeUUID(testAudience, testOrgID).String()
	wantMsgID := "zitadel-login:" + testUserAggID + ":" + testEventCreated

	pub := &mocks.MockPublisher{}
	pub.On("PublishRaw", mock.Anything, testCustomTopic,
		mock.MatchedBy(func(payload []byte) bool {
			var m publishedMsg
			if err := json.Unmarshal(payload, &m); err != nil {
				return false
			}
			return m.Type == "user" && m.Operation == "login" &&
				m.UserID == wantUser && m.TenantID == wantTenant && m.DataID == wantUser
		}),
		wantMsgID,
	).Return(nil).Once()

	rec := httptest.NewRecorder()
	zitadelwh.LoginHandler(testAudience, testSignKey, testStreamName, pub)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	pub.AssertExpectations(t)
}

func TestLoginHandler_RejectsBadSignature(t *testing.T) {
	t.Parallel()
	body := loginEventBody(testUserAggID)

	pub := &mocks.MockPublisher{} // no calls expected
	rec := httptest.NewRecorder()
	zitadelwh.LoginHandler(testAudience, testSignKey, testStreamName, pub)(rec, signedReq(t, body, "wrong-key"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	pub.AssertNotCalled(t, "PublishRaw", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestLoginHandler_SkipsPublishWhenAggregateMissing(t *testing.T) {
	t.Parallel()
	// resourceOwner present but aggregateID empty → nothing to publish, 200.
	body := loginEventBody("")

	pub := &mocks.MockPublisher{} // no calls expected
	rec := httptest.NewRecorder()
	zitadelwh.LoginHandler(testAudience, testSignKey, testStreamName, pub)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (missing aggregateID → skip, not error)", rec.Code)
	}
	pub.AssertNotCalled(t, "PublishRaw", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestLoginHandler_ReturnsOKWhenPublishFails(t *testing.T) {
	t.Parallel()
	// Fire-and-forget: a publish failure must NOT disturb Zitadel's event
	// projection, so the handler still returns 200.
	body := loginEventBody(testUserAggID)

	pub := &mocks.MockPublisher{}
	pub.On("PublishRaw", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(errors.New("publish failed")).Once()

	rec := httptest.NewRecorder()
	zitadelwh.LoginHandler(testAudience, testSignKey, testStreamName, pub)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when publish fails", rec.Code)
	}
	pub.AssertExpectations(t)
}

func TestLoginHandler_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	// Validly signed but not JSON → 400, and nothing published.
	body := []byte(`{not json`)

	pub := &mocks.MockPublisher{} // no calls expected
	rec := httptest.NewRecorder()
	zitadelwh.LoginHandler(testAudience, testSignKey, testStreamName, pub)(rec, signedReq(t, body, testSignKey))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on malformed JSON", rec.Code)
	}
	pub.AssertNotCalled(t, "PublishRaw", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestLoginHandler_BodyReadErrorReturns400(t *testing.T) {
	t.Parallel()
	// errReader (defined in actions_test.go) fails the io.ReadAll before the
	// signature check, short-circuiting to 400 with nothing published.
	pub := &mocks.MockPublisher{} // no calls expected
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhook/zitadel/login", errReader{})

	rec := httptest.NewRecorder()
	zitadelwh.LoginHandler(testAudience, testSignKey, testStreamName, pub)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on body read error", rec.Code)
	}
	pub.AssertNotCalled(t, "PublishRaw", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
