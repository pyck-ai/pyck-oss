package tenants

import "time"

// maxRedeliver caps the number of redelivery attempts before the
// message is dropped. After exhausting all backoff intervals, NATS
// considers the message terminal.
const maxRedeliver = 5

// redeliverBackoff defines exponential backoff intervals for Nak'd messages.
var redeliverBackoff = []time.Duration{
	2 * time.Second,
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

// zeroTimeStr is Go's zero time as serialized by Ent's JSON encoder.
var zeroTimeStr = time.Time{}.Format(time.RFC3339)

// isDeletedAt returns true when the deleted_at value represents an actual
// deletion timestamp (non-nil and not Go's zero time).
func isDeletedAt(v any) bool {
	if v == nil {
		return false
	}
	s, ok := v.(string)
	return !ok || s != zeroTimeStr
}
