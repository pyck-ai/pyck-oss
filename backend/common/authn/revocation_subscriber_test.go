//nolint:testpackage // handleRevocationMessage + topic.IsDeletedAt are intentionally unexported.
package authn

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/events/topic"
)

// fakeMsg is the smallest jetstream.Msg the handler under test reaches
// for: Subject() and Data() for decode + log fields, Ack() to confirm
// terminal disposition.
type fakeMsg struct {
	jetstream.Msg
	subject string
	data    []byte
	acked   bool
}

func (f *fakeMsg) Subject() string             { return f.subject }
func (f *fakeMsg) Data() []byte                { return f.data }
func (f *fakeMsg) Ack() error                  { f.acked = true; return nil }
func (f *fakeMsg) Nak() error                  { return nil }
func (f *fakeMsg) InProgress() error           { return nil }
func (f *fakeMsg) TermWithReason(string) error { return nil }

// topic.IsDeletedAt — Go's JSON encoding can decode a JSON number
// to float64 or a JSON bool to bool. The previous predicate returned
// true on any non-string non-nil value via the `!ok` shortcut, which
// would classify any future payload-shape drift as a "tenant disabled"
// signal and trigger a fleet-wide eviction. The fix returns false for
// non-string non-nil — only a real RFC3339 string distinguishable from
// the zero-time sentinel counts as a deletion.
func TestRevocationIsDeletedAt_NonStringNonNilReturnsFalse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		v    any
	}{
		{"number", float64(123456)},
		{"bool", true},
		{"slice", []any{"x"}},
		{"map", map[string]any{"time": "2026-01-01T00:00:00Z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.False(t, topic.IsDeletedAt(tc.v),
				"non-string non-nil deleted_at must NOT be classified as deleted — that would silently evict every cached token on a payload-shape drift")
		})
	}
}

// Sanity baselines: the obvious values stay correct.
func TestRevocationIsDeletedAt_StringPaths(t *testing.T) {
	t.Parallel()

	assert.False(t, topic.IsDeletedAt(nil), "nil = not deleted")
	assert.False(t, topic.IsDeletedAt(topic.ZeroTimeStr), "zero-time string = not deleted (Ent's 'unset' sentinel)")
	assert.True(t, topic.IsDeletedAt("2026-06-11T12:00:00Z"), "real RFC3339 string = deleted")
}

// Handler must NOT silently swallow events with non-map DataBefore/
// DataAfter. The previous code did `dataAfter, _ := …` and continued
// with a nil map; the eviction was lost and the Ack was final because
// the consumer is configured DeliverNewPolicy (no replay). The fix
// Error-logs and Acks (no point Nak'ing a malformed payload — the
// reconciler will eventually heal).
//
// We can't easily intercept the logger here, but we can assert the
// observable behaviour: the handler must NOT fire onDisabled when the
// data shape is wrong, and it must still Ack so the message isn't
// redelivered forever.
func TestHandleRevocationMessage_NonMapDataAfter_DoesNotEvict(t *testing.T) {
	t.Parallel()

	// Forge a payload where DataAfter is a STRING (not a map[string]any
	// as the schema expects). Pre-fix the silent ok-discard would
	// produce a nil map and skip eviction silently. Post-fix the
	// handler logs Error and Acks; the test asserts the visible
	// disposition (no callback, Ack=true).
	tenantID := uuid.New()
	rawEvent := topic.MutationEventMessage{
		Service:    topic.ManagementService,
		Schema:     topic.TenantSchema,
		Operation:  topic.OpUpdate,
		ID:         tenantID,
		DataBefore: map[string]any{"deleted_at": topic.ZeroTimeStr},
		DataAfter:  "not-a-map",
	}
	payload, err := json.Marshal(rawEvent)
	require.NoError(t, err)

	msg := &fakeMsg{
		subject: "pyck.management.crud.tenant.update." + tenantID.String(),
		data:    payload,
	}

	called := false
	handleRevocationMessage(context.Background(), msg, func(uuid.UUID) {
		called = true
	})

	assert.False(t, called, "malformed DataAfter must NOT trigger eviction")
	assert.True(t, msg.acked, "malformed events still get Ack'd — no point retrying a structural mismatch")
}

// Happy path: legit disable transition fires onDisabled and Acks.
func TestHandleRevocationMessage_DisableTransition_Evicts(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	rawEvent := topic.MutationEventMessage{
		Service:    topic.ManagementService,
		Schema:     topic.TenantSchema,
		Operation:  topic.OpUpdate,
		ID:         tenantID,
		DataBefore: map[string]any{"deleted_at": topic.ZeroTimeStr},
		DataAfter:  map[string]any{"deleted_at": "2026-06-11T12:00:00Z"},
	}
	payload, err := json.Marshal(rawEvent)
	require.NoError(t, err)

	msg := &fakeMsg{
		subject: "pyck.management.crud.tenant.update." + tenantID.String(),
		data:    payload,
	}

	var got uuid.UUID
	handleRevocationMessage(context.Background(), msg, func(id uuid.UUID) {
		got = id
	})

	assert.Equal(t, tenantID, got, "disable transition must fire onDisabled with the tenant ID")
	assert.True(t, msg.acked)
}
