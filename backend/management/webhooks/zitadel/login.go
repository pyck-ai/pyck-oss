package zitadel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	zitadelactions "github.com/zitadel/zitadel-go/v3/pkg/actions"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	httputil "github.com/pyck-ai/pyck/backend/common/http"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/std"
)

// loginEvent is the slice of a Zitadel Event-execution payload we use. For the
// human primary-auth events this Target subscribes to, the aggregate is the
// user: AggregateID is the user ID, ResourceOwner the home org. AggregateID +
// CreatedAt identify a single login, used for publish de-duplication.
//
// The mixed JSON casing is Zitadel's, not ours: the envelope fields are
// camelCase (aggregateID, aggregateType, resourceOwner) while the event
// metadata is snake_case (event_type, created_at). Tags mirror the wire
// format verbatim.
type loginEvent struct {
	AggregateID   string `json:"aggregateID"`
	AggregateType string `json:"aggregateType"`
	ResourceOwner string `json:"resourceOwner"`
	EventType     string `json:"event_type"`
	CreatedAt     string `json:"created_at"`
}

// LoginHandler handles the Zitadel Event-execution POST on human login. It
// verifies the ZITADEL-Signature HMAC, derives the pyck user/tenant UUIDs from
// the event's aggregate and resource owner (the same ComputeUUID derivation
// backend/common/authn uses server-side), and publishes a "user/login" event
// to NATS.
//
// audience must match common/authn (the Zitadel issuer). signingKey must be
// non-empty — the caller only mounts this route when a key is set, and every
// request is verified. Fire-and-forget: returns 200 except on a malformed or
// unauthenticated request, so a failed publish never blocks login.
func LoginHandler(audience, signingKey, streamName string, publisher events.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := log.ForContext(ctx)

		body, err := io.ReadAll(r.Body)
		if err != nil {
			httputil.JSONError(w, "read body", http.StatusBadRequest)
			return
		}

		if err := zitadelactions.ValidateRequestPayload(body, &r.Header, signingKey); err != nil {
			logger.Warn().Err(err).Msg("zitadel login action signature rejected")
			httputil.JSONError(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var ev loginEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			logger.Warn().Err(err).Msg("zitadel login action payload not JSON")
			httputil.JSONError(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		if ev.AggregateID == "" || ev.ResourceOwner == "" {
			// No user/org context — nothing to publish. 200 so Zitadel's event
			// projection isn't disturbed. Logged to surface payload-shape drift.
			logger.Warn().
				Str("event_type", ev.EventType).
				Msg("zitadel login action: missing aggregateID/resourceOwner; skipping publish")
			w.WriteHeader(http.StatusOK)
			return
		}

		userID := authn.ComputeUUID(audience, ev.AggregateID)
		tenantID := authn.ComputeUUID(audience, ev.ResourceOwner)

		payload, err := std.MarshalJson(events.CustomEventMessage{
			Type:      "user",
			Operation: "login",
			TenantID:  tenantID,
			UserID:    userID,
			DataID:    userID,
		})
		if err != nil {
			logger.Error().Err(err).Msg("zitadel login action: failed to marshal login event")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Stable per-login msg ID lets JetStream collapse duplicates if Zitadel
		// re-delivers the same event (at-least-once / retries).
		msgID := fmt.Sprintf("zitadel-login:%s:%s", ev.AggregateID, ev.CreatedAt)
		topic := events.CustomEventTopic{StreamName: streamName}.String()

		if err := publisher.PublishRaw(ctx, topic, payload, msgID); err != nil {
			logger.Error().Err(err).
				Str("user_id", userID.String()).
				Str("tenant_id", tenantID.String()).
				Msg("zitadel login action: failed to publish login event")
			w.WriteHeader(http.StatusOK)
			return
		}

		logger.Debug().
			Str("event_type", ev.EventType).
			Str("user_id", userID.String()).
			Str("tenant_id", tenantID.String()).
			Msg("published user login event")
		w.WriteHeader(http.StatusOK)
	}
}
