package topic

import "time"

// ManagementService and TenantSchema identify the source of
// tenant-lifecycle CRUD events. Shared so a rename lands in one place.
const (
	ManagementService = "management"
	TenantSchema      = "tenant"
)

// ZeroTimeStr mirrors Ent's JSON encoding of Go's zero time. Outbox
// payloads carry deleted_at = ZeroTimeStr when the row is NOT deleted.
var ZeroTimeStr = time.Time{}.Format(time.RFC3339)

// IsDeletedAt reports whether the deleted_at value represents a real
// deletion. nil, the zero-time sentinel, and any non-string value all
// mean "not deleted" — fail closed against payload-shape drift.
func IsDeletedAt(v any) bool {
	if v == nil {
		return false
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return s != ZeroTimeStr
}
