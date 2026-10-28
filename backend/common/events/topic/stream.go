package topic

import (
	"strings"

	"github.com/google/uuid"
)

// Stream names derive from the configured stream name (StreamName of a topic,
// DefaultStreamName when empty), here called the prefix; see
// backend/common/events/streams.go for the layout.

// AggregateStreamName returns the name of the stream the services consume.
func AggregateStreamName(prefix string) string {
	return getStreamName(prefix)
}

// TenantStreamName returns the name of tenantID's stream.
func TenantStreamName(prefix string, tenantID uuid.UUID) string {
	return getStreamName(prefix) + "-" + tenantID.String()
}

// TenantStreamSubjects returns the subjects of tenantID's stream.
func TenantStreamSubjects(prefix string, tenantID uuid.UUID) string {
	return formatTopic(getStreamName(prefix), tenantID) + ".>"
}

// ParseTenantStreamName returns the tenant of a tenant stream name of prefix.
func ParseTenantStreamName(prefix, name string) (uuid.UUID, bool) {
	raw, ok := strings.CutPrefix(name, getStreamName(prefix)+"-")
	if !ok {
		return uuid.Nil, false
	}

	return canonicalTenantID(raw)
}

// SystemStreamName returns the name of the stream of events of no tenant. It is
// also the stream token of their subjects, so it is the StreamName of a system
// topic.
func SystemStreamName(prefix string) string {
	return getStreamName(prefix) + "-system"
}

// SystemStreamSubjects returns the subjects of the system stream.
func SystemStreamSubjects(prefix string) string {
	return formatTopic(SystemStreamName(prefix)) + ".>"
}

// ParseTenantNamespace returns the tenant a Temporal namespace belongs to: a
// tenant's namespace is its ID in canonical form, any other ("default", the nil
// UUID, ...) is no tenant's.
func ParseTenantNamespace(namespace string) (uuid.UUID, bool) {
	return canonicalTenantID(namespace)
}

func canonicalTenantID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil || id.String() != s {
		return uuid.Nil, false
	}

	return id, true
}
