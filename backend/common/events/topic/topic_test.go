package topic

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// All comprehensive tests for topic matching and parsing are implemented below

func TestMatchesPattern_EdgeCases(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		subject       string
		pattern       topicPattern
		shouldMatch   bool
		expectedField map[string]string
	}{
		{
			name:    "consecutive wildcards in subject",
			subject: "pyck.*.crud.*.users",
			pattern: topicPattern{
				typ: TopicTypeMutationEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
					{"crud", true, fieldTypeString},
					{"service", false, fieldTypeString},
					{"schema", false, fieldTypeString},
				},
			},
			shouldMatch: true, // token count matches (5 tokens)
			expectedField: map[string]string{
				"stream":  "pyck",
				"tenant":  "*",
				"service": "*",
				"schema":  "users",
			},
		},
		{
			name:    "full wildcard at non-terminal position",
			subject: "pyck.>.crud.users",
			pattern: topicPattern{
				typ: TopicTypeMutationEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
					{"crud", true, fieldTypeString},
				},
			},
			shouldMatch: false, // '>' must be last token
		},
		{
			name:    "full wildcard at terminal position",
			subject: "pyck.tenant.>",
			pattern: topicPattern{
				typ: TopicTypeMutationEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
					{"crud", true, fieldTypeString},
					{"service", false, fieldTypeString},
				},
			},
			shouldMatch: true,
			expectedField: map[string]string{
				"stream":  "pyck",
				"tenant":  "tenant",
				"service": "*", // only non-literal fields after '>' get wildcard
			},
		},
		{
			name:    "single wildcard matching any token",
			subject: "pyck.*.crud",
			pattern: topicPattern{
				typ: TopicTypeCustomEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
					{"crud", true, fieldTypeString},
				},
			},
			shouldMatch: true,
			expectedField: map[string]string{
				"stream": "pyck",
				"tenant": "*",
			},
		},
		{
			name:    "pattern longer than subject",
			subject: "pyck.tenant",
			pattern: topicPattern{
				typ: TopicTypeMutationEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
					{"crud", true, fieldTypeString},
					{"service", false, fieldTypeString},
				},
			},
			shouldMatch: false,
		},
		{
			name:    "pattern shorter than subject",
			subject: "pyck.tenant.crud.service.extra",
			pattern: topicPattern{
				typ: TopicTypeCustomEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
				},
			},
			shouldMatch: false,
		},
		{
			name:    "literal token mismatch",
			subject: "pyck.tenant.invalid.service",
			pattern: topicPattern{
				typ: TopicTypeMutationEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
					{"crud", true, fieldTypeString},
					{"service", false, fieldTypeString},
				},
			},
			shouldMatch: false, // "invalid" doesn't match literal "crud"
		},
		{
			name:    "wildcard in literal position",
			subject: "pyck.tenant.*.service",
			pattern: topicPattern{
				typ: TopicTypeMutationEvent,
				tokens: []topicToken{
					{"stream", false, fieldTypeString},
					{"tenant", false, fieldTypeUUID},
					{"crud", true, fieldTypeString},
					{"service", false, fieldTypeString},
				},
			},
			shouldMatch: true, // wildcard matches literal
			expectedField: map[string]string{
				"stream":  "pyck",
				"tenant":  "tenant",
				"service": "service",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fields, matched := matchesPattern(tc.subject, tc.pattern)

			if matched != tc.shouldMatch {
				t.Errorf("Expected match=%v, got match=%v", tc.shouldMatch, matched)
			}

			if tc.shouldMatch && tc.expectedField != nil {
				for key, expectedVal := range tc.expectedField {
					if actualVal, ok := fields[key]; !ok {
						t.Errorf("Expected field %q not found in result", key)
					} else if actualVal != expectedVal {
						t.Errorf("Field %q: expected %q, got %q", key, expectedVal, actualVal)
					}
				}
			}
		})
	}
}

func TestParse_AmbiguousTopic(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name              string
		topic             string
		shouldBeAmbiguous bool
		expectedTypes     []string
	}{
		{
			name:              "unambiguous custom event",
			topic:             "pyck.custom-events",
			shouldBeAmbiguous: false,
		},
		{
			name:              "unambiguous mutation event",
			topic:             "pyck.00000000-0000-0000-0000-000000000001.crud.management.users.00000000-0000-0000-0000-000000000002.create",
			shouldBeAmbiguous: false,
		},
		{
			name:              "unambiguous update event - has extra attribute field",
			topic:             "pyck.00000000-0000-0000-0000-000000000001.crud.management.users.00000000-0000-0000-0000-000000000002.update.email",
			shouldBeAmbiguous: false,
		},
		{
			name:              "unambiguous workflow event",
			topic:             "pyck.00000000-0000-0000-0000-000000000001.workflows.00000000-0000-0000-0000-000000000002.MyWorkflow",
			shouldBeAmbiguous: false,
		},
		{
			name:              "unambiguous temporal workflow event",
			topic:             "pyck.my-namespace.temporal.my-queue.MyWorkflow.wf-123.run-456.completed",
			shouldBeAmbiguous: false,
		},
		{
			name:              "unambiguous mutation with reply",
			topic:             "request.reply.pyck.00000000-0000-0000-0000-000000000001.crud.management.users.00000000-0000-0000-0000-000000000002.create",
			shouldBeAmbiguous: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := Parse(tc.topic)

			if !tc.shouldBeAmbiguous {
				require.NoError(t, err)
				require.NotNil(t, result)
				return
			}

			// Handle ambiguous cases
			require.Error(t, err, "Expected an error for ambiguous topic, but got none")
			require.ErrorIs(t, err, ErrAmbiguousTopic, "Expected ErrAmbiguousTopic")

			// Verify error message includes expected types
			errMsg := err.Error()
			for _, expectedType := range tc.expectedTypes {
				assert.Contains(t, errMsg, expectedType, "Error message should contain expected type")
			}
		})
	}
}

func TestWildcardPrecedence(t *testing.T) {
	t.Parallel()
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	testCases := []struct {
		name        string
		pattern     Topic
		subject     Topic
		shouldMatch bool
		description string
	}{
		{
			name: "uuid.Nil wildcard matches concrete UUID",
			pattern: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      uuid.Nil, // wildcard
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			subject: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID, // concrete
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: true,
			description: "pattern with wildcard tenant matches subject with concrete tenant",
		},
		{
			name: "concrete UUID does not match different UUID",
			pattern: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID, // concrete
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			subject: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      uuid.MustParse("00000000-0000-0000-0000-000000000003"), // different
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: false,
			description: "concrete UUIDs must match exactly",
		},
		{
			name: "empty string wildcard matches concrete string",
			pattern: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "", // wildcard
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			subject: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management", // concrete
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: true,
			description: "pattern with empty service matches subject with concrete service",
		},
		{
			name: "wildcards asymmetric - pattern wildcards match subject concretes",
			pattern: MutationEventTopic{
				StreamName:    "",       // wildcard
				TenantID:      uuid.Nil, // wildcard
				ServiceName:   "",       // wildcard
				SchemaName:    "",       // wildcard
				EntityID:      uuid.Nil, // wildcard
				OperationName: "",       // wildcard
			},
			subject: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			shouldMatch: true,
			description: "fully wildcarded pattern matches any concrete subject",
		},
		{
			name: "subject wildcards do not match pattern literals",
			pattern: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			subject: MutationEventTopic{
				StreamName:    "",       // wildcard in subject
				TenantID:      uuid.Nil, // wildcard in subject
				ServiceName:   "",       // wildcard in subject
				SchemaName:    "",       // wildcard in subject
				EntityID:      uuid.Nil, // wildcard in subject
				OperationName: "",       // wildcard in subject
			},
			shouldMatch: false,
			description: "subject wildcards do NOT match pattern literals (asymmetric)",
		},
		{
			name: "both wildcards match",
			pattern: MutationEventTopic{
				StreamName:    "",
				TenantID:      uuid.Nil,
				ServiceName:   "",
				SchemaName:    "",
				EntityID:      uuid.Nil,
				OperationName: "",
			},
			subject: MutationEventTopic{
				StreamName:    "",
				TenantID:      uuid.Nil,
				ServiceName:   "",
				SchemaName:    "",
				EntityID:      uuid.Nil,
				OperationName: "",
			},
			shouldMatch: true,
			description: "two fully wildcarded topics match each other",
		},
		{
			name: "mixed wildcards and concrete values",
			pattern: WorkflowEventTopic{
				StreamName:   "pyck",
				TenantID:     uuid.Nil, // wildcard
				WorkflowID:   uuid.MustParse("00000000-0000-0000-0000-000000000003"),
				WorkflowName: "", // wildcard
			},
			subject: WorkflowEventTopic{
				StreamName:   "pyck",
				TenantID:     tenantID,
				WorkflowID:   uuid.MustParse("00000000-0000-0000-0000-000000000003"),
				WorkflowName: "MyWorkflow",
			},
			shouldMatch: true,
			description: "pattern with some wildcards matches subject",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			matched := tc.pattern.Matches(tc.subject)

			if matched != tc.shouldMatch {
				t.Errorf("Expected match=%v, got match=%v\nDescription: %s", tc.shouldMatch, matched, tc.description)
			}
		})
	}
}

func TestTopicValidation(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		subject string
		isValid bool
	}{
		// Valid subjects
		{name: "simple topic", subject: "pyck.custom-events", isValid: true},
		{name: "with single wildcard", subject: "pyck.*.crud", isValid: true},
		{name: "with full wildcard at end", subject: "pyck.tenant.>", isValid: true},
		{name: "multiple single wildcards", subject: "*.*.*.>", isValid: true},
		{name: "complex topic", subject: "pyck.tenant.crud.service.schema.entity.operation", isValid: true},
		{name: "single token", subject: "pyck", isValid: true},
		{name: "just full wildcard", subject: ">", isValid: true},
		{name: "just single wildcard", subject: "*", isValid: true},
		{name: "hyphenated tokens", subject: "pyck.custom-events.my-service", isValid: true},
		{name: "underscores in tokens", subject: "pyck.my_service.my_entity", isValid: true},
		{name: "numbers in tokens", subject: "pyck.service123.entity456", isValid: true},
		{name: "uuid-like tokens", subject: "pyck.00000000-0000-0000-0000-000000000001.crud", isValid: true},

		// Invalid subjects
		{name: "empty subject", subject: "", isValid: false},
		{name: "consecutive dots", subject: "pyck..crud", isValid: false},
		{name: "leading dot", subject: ".pyck.crud", isValid: false},
		{name: "trailing dot", subject: "pyck.crud.", isValid: false},
		{name: "null byte", subject: "pyck\x00crud", isValid: false},
		{name: "null byte in token", subject: "pyck.te\x00nant.crud", isValid: false},
		{name: "space in token", subject: "pyck.my service.crud", isValid: false},
		{name: "tab in token", subject: "pyck.tenant\t.crud", isValid: false},
		{name: "newline in token", subject: "pyck\n.tenant", isValid: false},
		{name: "full wildcard not at end", subject: "pyck.>.crud", isValid: false},
		{name: "full wildcard in middle", subject: "*.>.crud", isValid: false},
		{name: "wildcard mixed with text", subject: "pyck.tenant*.crud", isValid: false},
		{name: "wildcard mixed with text 2", subject: "pyck.*tenant.crud", isValid: false},
		{name: "full wildcard mixed with text", subject: "pyck.>tenant.crud", isValid: false},
		{name: "multiple consecutive wildcards in token", subject: "pyck.**.crud", isValid: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := IsValidSubscriptionSubject(tc.subject)

			if result != tc.isValid {
				t.Errorf("IsValidSubscriptionSubject(%q) = %v, expected %v", tc.subject, result, tc.isValid)
			}
		})
	}
}

func TestTopicRoundTrip(t *testing.T) {
	t.Parallel()
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	workflowID := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	testCases := []struct {
		name  string
		topic Topic
	}{
		{
			name: "CustomEventTopic",
			topic: CustomEventTopic{
				StreamName: "pyck",
			},
		},
		{
			name: "MutationEventTopic",
			topic: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
		},
		{
			name: "MutationEventWithReplyTopic",
			topic: MutationEventWithReplyTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "update",
			},
		},
		{
			name: "UpdateEventTopic",
			topic: UpdateEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "update",
				AttributeName: "email",
			},
		},
		{
			name: "WorkflowEventTopic",
			topic: WorkflowEventTopic{
				StreamName:   "pyck",
				TenantID:     tenantID,
				WorkflowID:   workflowID,
				WorkflowName: "myworkflow", // lowercase since formatTopicPart lowercases
			},
		},
		{
			name: "TemporalWorkflowStateChangeTopic",
			topic: TemporalWorkflowStateChangeTopic{
				StreamName:       "pyck",
				Namespace:        "my-namespace",
				TaskQueue:        "my-queue",
				WorkflowTypeName: "myworkflow", // lowercase since formatTopicPart lowercases
				WorkflowID:       "wf-123",
				RunID:            "run-456",
				Status:           "completed",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Convert to string
			topicString := tc.topic.String()

			// Parse back
			parsed, err := Parse(topicString)
			if err != nil {
				t.Fatalf("Failed to parse topic string %q: %v", topicString, err)
			}

			// Verify type matches
			if parsed.Type() != tc.topic.Type() {
				t.Errorf("Type mismatch: original=%v, parsed=%v", tc.topic.Type(), parsed.Type())
			}

			// Verify string representation matches
			if parsed.String() != topicString {
				t.Errorf("String mismatch: original=%q, parsed=%q", topicString, parsed.String())
			}

			// Verify they match each other
			if !tc.topic.Matches(parsed) {
				t.Errorf("Original topic does not match parsed topic")
			}
			if !parsed.Matches(tc.topic) {
				t.Errorf("Parsed topic does not match original topic")
			}
		})
	}
}

func TestTopicMatches_Symmetry(t *testing.T) {
	t.Parallel()
	tenantID1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tenantID2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	entityID := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	testCases := []struct {
		name        string
		topicA      Topic
		topicB      Topic
		aMatchesB   bool
		bMatchesA   bool
		description string
	}{
		{
			name: "identical topics - symmetric",
			topicA: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID1,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			topicB: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID1,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			aMatchesB:   true,
			bMatchesA:   true,
			description: "identical topics match symmetrically",
		},
		{
			name: "wildcard in A, concrete in B - asymmetric",
			topicA: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      uuid.Nil, // wildcard
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			topicB: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID1, // concrete
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			aMatchesB:   true,  // wildcard matches concrete
			bMatchesA:   false, // concrete doesn't match wildcard
			description: "wildcard asymmetry: pattern wildcards match subject concretes, but not vice versa",
		},
		{
			name: "different concrete values - no match",
			topicA: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID1,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			topicB: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID2, // different
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			aMatchesB:   false,
			bMatchesA:   false,
			description: "different concrete values don't match in either direction",
		},
		{
			name: "fully wildcarded A - matches any B",
			topicA: MutationEventTopic{
				StreamName:    "",
				TenantID:      uuid.Nil,
				ServiceName:   "",
				SchemaName:    "",
				EntityID:      uuid.Nil,
				OperationName: "",
			},
			topicB: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID1,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			aMatchesB:   true,  // wildcard matches anything
			bMatchesA:   false, // concrete doesn't match wildcard
			description: "fully wildcarded topic matches any concrete topic (asymmetric)",
		},
		{
			name: "both wildcarded - symmetric",
			topicA: MutationEventTopic{
				StreamName:    "",
				TenantID:      uuid.Nil,
				ServiceName:   "",
				SchemaName:    "",
				EntityID:      uuid.Nil,
				OperationName: "",
			},
			topicB: MutationEventTopic{
				StreamName:    "",
				TenantID:      uuid.Nil,
				ServiceName:   "",
				SchemaName:    "",
				EntityID:      uuid.Nil,
				OperationName: "",
			},
			aMatchesB:   true,
			bMatchesA:   true,
			description: "both wildcarded topics match symmetrically",
		},
		{
			name: "different topic types - no match",
			topicA: CustomEventTopic{
				StreamName: "pyck",
			},
			topicB: MutationEventTopic{
				StreamName:    "pyck",
				TenantID:      tenantID1,
				ServiceName:   "management",
				SchemaName:    "users",
				EntityID:      entityID,
				OperationName: "create",
			},
			aMatchesB:   false,
			bMatchesA:   false,
			description: "different topic types never match",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Test A.Matches(B)
			if result := tc.topicA.Matches(tc.topicB); result != tc.aMatchesB {
				t.Errorf("A.Matches(B): expected %v, got %v\nDescription: %s", tc.aMatchesB, result, tc.description)
			}

			// Test B.Matches(A)
			if result := tc.topicB.Matches(tc.topicA); result != tc.bMatchesA {
				t.Errorf("B.Matches(A): expected %v, got %v\nDescription: %s", tc.bMatchesA, result, tc.description)
			}
		})
	}
}
