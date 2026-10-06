package topic

import "github.com/google/uuid"

// MutationEventMessage is the JSON-decoded payload carried by every NATS
// message published on a MutationEventTopic. Kept here in the leaf package so
// subscribers outside backend/common/events (e.g. authn revocation) can decode
// the payload without importing the full events package and triggering an
// import cycle.
type MutationEventMessage struct {
	Service   string    `json:"service"`
	Type      string    `json:"type"`
	Schema    string    `json:"schema"`
	Operation string    `json:"operation"`
	ID        uuid.UUID `json:"id"`
	// EventID identifies this event: the ID of the outbox row that carries it,
	// stable across publish retries. ID above is the entity ID, which two
	// updates of one entity share, so consumers that deduplicate use EventID.
	EventID            uuid.UUID         `json:"event_id"`
	TenantID           uuid.UUID         `json:"tenant_id"`
	DataBefore         any               `json:"data_before,omitempty"`
	DataAfter          any               `json:"data_after"`
	Namespace          string            `json:"namespace"`
	WfSearchAttributes map[string]string `json:"wf_search_attributes"`
}
