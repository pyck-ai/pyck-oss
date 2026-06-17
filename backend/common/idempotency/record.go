package idempotency

import (
	"time"

	"github.com/google/uuid"
)

// Status is the lifecycle state of an idempotency record.
type Status string

const (
	// StatusInFlight indicates the originating mutation is still running.
	// A second request with the same key while in-flight is rejected as 409.
	StatusInFlight Status = "in_flight"
	// StatusCommitted indicates the mutation transaction committed and the
	// response was cached. Subsequent requests with the same key replay
	// the cached body and return 200.
	StatusCommitted Status = "committed"
)

// Record is the value stored per (key, tenant, user) tuple.
type Record struct {
	Key               string
	TenantID          uuid.UUID
	UserID            uuid.UUID
	OperationName     string
	OperationChecksum [32]byte
	Status            Status
	// Response is the serialized GraphQL response body. Populated only when
	// Status == StatusCommitted.
	Response  []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}
