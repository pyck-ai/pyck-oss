package topic_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/events/topic"
)

func TestStreamNames(t *testing.T) {
	t.Parallel()

	tenantID := uuid.MustParse("0190a6e1-7b2c-7d3e-8f40-123456789abc")

	assert.Equal(t, "pyck", topic.AggregateStreamName(""))
	assert.Equal(t, "pyck-0190a6e1-7b2c-7d3e-8f40-123456789abc", topic.TenantStreamName("pyck", tenantID))
	assert.Equal(t, "pyck.0190a6e1-7b2c-7d3e-8f40-123456789abc.>", topic.TenantStreamSubjects("pyck", tenantID))
	assert.Equal(t, "pyck-system", topic.SystemStreamName(""))
	assert.Equal(t, "pyck-system.>", topic.SystemStreamSubjects("pyck"))

	// A tenant's topics land in its stream.
	mutation := topic.MutationEventTopic{StreamName: "pyck", TenantID: tenantID, ServiceName: "s", SchemaName: "x", EntityID: uuid.New(), OperationName: "create"}
	assert.True(t, strings.HasPrefix(mutation.String(), strings.TrimSuffix(topic.TenantStreamSubjects("pyck", tenantID), ">")))
	assert.Equal(t, "pyck-system.custom-events", topic.CustomEventTopic{StreamName: topic.SystemStreamName("pyck")}.String())
}

func TestParseTenantStreamName(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()

	for name, tc := range map[string]struct {
		stream string
		want   uuid.UUID
		ok     bool
	}{
		"tenant stream":       {stream: topic.TenantStreamName("pyck", tenantID), want: tenantID, ok: true},
		"system stream":       {stream: topic.SystemStreamName("pyck")},
		"aggregate":           {stream: "pyck"},
		"other prefix":        {stream: topic.TenantStreamName("other", tenantID)},
		"key-value bucket":    {stream: "KV_pyck_routing_status"},
		"uppercase tenant ID": {stream: "pyck-" + "0190A6E1-7B2C-7D3E-8F40-123456789ABC"},
		"nil tenant ID":       {stream: topic.TenantStreamName("pyck", uuid.Nil)},
		"suffix after the ID": {stream: topic.TenantStreamName("pyck", tenantID) + "-x"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := topic.ParseTenantStreamName("pyck", tc.stream)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseTenantNamespace(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()

	got, ok := topic.ParseTenantNamespace(tenantID.String())
	assert.True(t, ok)
	assert.Equal(t, tenantID, got)

	for _, namespace := range []string{"default", "temporal-system", "", uuid.Nil.String(), "0190A6E1-7B2C-7D3E-8F40-123456789ABC", "0190a6e17b2c7d3e8f40123456789abc"} {
		_, ok := topic.ParseTenantNamespace(namespace)
		assert.False(t, ok, namespace)
	}
}
