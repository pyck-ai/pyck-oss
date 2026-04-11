package services_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/workflow/services"
)

// TODO(michael): Integration tests needed for concurrent event handling, Temporal client failures, and NATS disconnection/reconnection

func TestSignalRouter_FilterRuleEvaluation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create a minimal SignalRouter just for testing evalFilterRule
	router := &services.SignalRouter{}

	testCases := []struct {
		name          string
		filterRule    string
		data          any
		shouldMatch   bool
		expectError   bool
		errorContains string
	}{
		{
			name:        "empty filter rule - should not match",
			filterRule:  "",
			data:        map[string]any{"status": "active"},
			shouldMatch: false,
			expectError: false,
		},
		{
			name:        "simple equality - match",
			filterRule:  `status = "active"`,
			data:        map[string]any{"status": "active"},
			shouldMatch: true,
			expectError: false,
		},
		{
			name:        "simple equality - no match",
			filterRule:  `status = "active"`,
			data:        map[string]any{"status": "inactive"},
			shouldMatch: false,
			expectError: false,
		},
		{
			name:        "numeric comparison - match",
			filterRule:  `quantity > 100`,
			data:        map[string]any{"quantity": 150},
			shouldMatch: true,
			expectError: false,
		},
		{
			name:        "numeric comparison - no match",
			filterRule:  `quantity > 100`,
			data:        map[string]any{"quantity": 50},
			shouldMatch: false,
			expectError: false,
		},
		{
			name:        "complex AND condition - match",
			filterRule:  `status = "active" and quantity > 100`,
			data:        map[string]any{"status": "active", "quantity": 150},
			shouldMatch: true,
			expectError: false,
		},
		{
			name:        "complex AND condition - no match",
			filterRule:  `status = "active" and quantity > 100`,
			data:        map[string]any{"status": "active", "quantity": 50},
			shouldMatch: false,
			expectError: false,
		},
		{
			name:        "complex OR condition - match first",
			filterRule:  `status = "active" or status = "pending"`,
			data:        map[string]any{"status": "active"},
			shouldMatch: true,
			expectError: false,
		},
		{
			name:        "complex OR condition - match second",
			filterRule:  `status = "active" or status = "pending"`,
			data:        map[string]any{"status": "pending"},
			shouldMatch: true,
			expectError: false,
		},
		{
			name:        "complex OR condition - no match",
			filterRule:  `status = "active" or status = "pending"`,
			data:        map[string]any{"status": "cancelled"},
			shouldMatch: false,
			expectError: false,
		},
		{
			name:        "nested field access - match",
			filterRule:  `user.role = "admin"`,
			data:        map[string]any{"user": map[string]any{"role": "admin"}},
			shouldMatch: true,
			expectError: false,
		},
		{
			name:          "invalid FEEL syntax",
			filterRule:    `status = `,
			data:          map[string]any{"status": "active"},
			shouldMatch:   false,
			expectError:   true,
			errorContains: "failed to parse FEEL expression",
		},
		{
			name:        "missing field in data - evaluates to false",
			filterRule:  `nonexistent = "value"`,
			data:        map[string]any{"status": "active"},
			shouldMatch: false,
			expectError: false,
		},
		{
			name:          "non-boolean result",
			filterRule:    `quantity + 10`,
			data:          map[string]any{"quantity": 100},
			shouldMatch:   false,
			expectError:   true,
			errorContains: "failed to evaluate FEEL expression",
		},
		{
			name:        "boolean true literal",
			filterRule:  `true`,
			data:        map[string]any{},
			shouldMatch: true,
			expectError: false,
		},
		{
			name:        "boolean false literal",
			filterRule:  `false`,
			data:        map[string]any{},
			shouldMatch: false,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			matched, err := router.EvalFilterRule(ctx, tc.filterRule, tc.data)

			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tc.errorContains)
				} else if !strings.Contains(err.Error(), tc.errorContains) {
					t.Errorf("Expected error containing %q, got: %v", tc.errorContains, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if matched != tc.shouldMatch {
				t.Errorf("Expected match=%v, got match=%v", tc.shouldMatch, matched)
			}
		})
	}
}

func TestSignalRouter_WorkflowSignalMatching(t *testing.T) {
	t.Parallel()
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	testCases := []struct {
		name        string
		signalTopic string
		eventTopic  events.Topic
		shouldMatch bool
		description string
	}{
		{
			name:        "exact match - concrete values",
			signalTopic: "pyck.00000000-0000-0000-0000-000000000001.crud.management.users.00000000-0000-0000-0000-000000000002.create",
			eventTopic: events.MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: true,
			description: "Exact topic match with all concrete values",
		},
		{
			name:        "wildcard tenant - should match",
			signalTopic: "pyck.*.crud.management.users.*.create",
			eventTopic: events.MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: true,
			description: "Wildcard in signal matches concrete tenant in event",
		},
		{
			name:        "wildcard operation - match any operation",
			signalTopic: "pyck.00000000-0000-0000-0000-000000000001.crud.management.users.00000000-0000-0000-0000-000000000002.*",
			eventTopic: events.MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "update",
			},
			shouldMatch: true,
			description: "Wildcard operation matches any operation type",
		},
		{
			name:        "multiple wildcards - match many fields",
			signalTopic: "pyck.00000000-0000-0000-0000-000000000001.crud.*.*.*.create",
			eventTopic: events.MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: true,
			description: "Multiple wildcards match their respective positions",
		},
		{
			name:        "different tenant - no match",
			signalTopic: "pyck.00000000-0000-0000-0000-000000000099.crud.management.users.*.create",
			eventTopic: events.MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: false,
			description: "Different tenant ID should not match",
		},
		{
			name:        "different service - no match",
			signalTopic: "pyck.*.crud.inventory.users.*.create",
			eventTopic: events.MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: false,
			description: "Different service name should not match",
		},
		{
			name:        "workflow event - exact match",
			signalTopic: "pyck.00000000-0000-0000-0000-000000000001.workflows.00000000-0000-0000-0000-000000000003.myworkflow",
			eventTopic: events.WorkflowEventTopic{
				StreamName:   "pyck",
				TenantID:     tenantID,
				WorkflowID:   uuid.MustParse("00000000-0000-0000-0000-000000000003"),
				WorkflowName: "myworkflow", // lowercase
			},
			shouldMatch: true,
			description: "Workflow event exact match",
		},
		{
			name:        "workflow event - wildcard workflow name",
			signalTopic: "pyck.00000000-0000-0000-0000-000000000001.workflows.00000000-0000-0000-0000-000000000003.*",
			eventTopic: events.WorkflowEventTopic{
				StreamName:   "pyck",
				TenantID:     tenantID,
				WorkflowID:   uuid.MustParse("00000000-0000-0000-0000-000000000003"),
				WorkflowName: "anyworkflow",
			},
			shouldMatch: true,
			description: "Wildcard workflow name matches any workflow",
		},
		{
			name:        "fully wildcarded signal - matches everything",
			signalTopic: "*.*.*.*.*.*.*",
			eventTopic: events.MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: true,
			description: "Fully wildcarded signal matches any event",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Parse the signal topic
			signalTopicParsed, err := events.Parse(tc.signalTopic)
			if err != nil {
				t.Fatalf("Failed to parse signal topic: %v", err)
			}

			// Check if signal matches event
			matched := signalTopicParsed.Matches(tc.eventTopic)

			if matched != tc.shouldMatch {
				t.Errorf("Expected match=%v, got match=%v\nDescription: %s\nSignal: %s\nEvent: %s",
					tc.shouldMatch, matched, tc.description, tc.signalTopic, tc.eventTopic.String())
			}
		})
	}
}
