//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package topic

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	DefaultStreamName = "pyck"
)

var (
	ErrInvalidTopic     = errors.New("invalid topic format")
	ErrUnknownTopicType = errors.New("unknown topic type")
	ErrInvalidUUID      = errors.New("invalid UUID in topic")
	ErrAmbiguousTopic   = errors.New("topic matches multiple types")
	ErrUnknownFieldType = errors.New("unknown field type")
)

// TopicType represents the type of NATS topic.
//
//go:generate enumer -output=topic_gen.go -type=TopicType -trimprefix=TopicType
type TopicType int

const (
	// TopicTypeUnknown is the default topic type.
	TopicTypeUnknown TopicType = iota

	// TopicTypeCustomEvent represents a custom event topic.
	// Pattern: <stream>.custom-events
	TopicTypeCustomEvent

	// TopicTypeMutationEvent represents a CRUD mutation event.
	// Pattern: <stream>.<tenant>.crud.<service>.<schema>.<entity>.<operation>
	TopicTypeMutationEvent

	// TopicTypeDeadLetterEvent represents a mutation event that exhausted its
	// publish retries and was dead-lettered.
	// Pattern: <stream>.<tenant>.dlq.<service>.<schema>.<entity>.<operation>
	TopicTypeDeadLetterEvent

	// TopicTypeMutationEventWithReply represents a CRUD mutation event with request-reply pattern.
	// Pattern: request.reply.<stream>.<tenant>.crud.<service>.<schema>.<entity>.<operation>
	TopicTypeMutationEventWithReply

	// TopicTypeUpdateEvent represents an entity attribute update event.
	// Pattern: <stream>.<tenant>.crud.<service>.<schema>.<entity>.<operation>.<attribute>
	TopicTypeUpdateEvent

	// TopicTypeWorkflowEvent represents a workflow-related event.
	// Pattern: <stream>.<tenant>.workflows.<workflowID>.<workflowName>
	TopicTypeWorkflowEvent

	// TopicTypeTemporalWorkflowStateChangeEvent represents a temporal workflow state change event.
	// Pattern: <stream>.<namespace>.temporal.<workflowID>.<runID>.<status>
	TopicTypeTemporalWorkflowStateChangeEvent
)

// Topic is the interface that all topic types implement.
type Topic interface {
	// String returns the NATS subject in string form.
	String() string
	// Type returns the topic type.
	Type() TopicType
	// Valid checks if the topic string is a valid NATS topic.
	Valid() bool
	// Matches checks if other matches this topic.
	// Wildcard in this topic match any value in other, but not vice versa.
	Matches(other Topic) bool
}

// Validate checks if a topic string matches any known pattern.
func Validate(topic string) error {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return fmt.Errorf("%w: empty topic", ErrInvalidTopic)
	}

	// Check if any pattern matches
	for _, pattern := range topicPatterns {
		if _, ok := matchesPattern(topic, pattern); ok {
			return nil
		}
	}

	return fmt.Errorf("%w: %s", ErrUnknownTopicType, topic)
}

// Parse parses a topic string and returns the appropriate Topic type.
// Returns ErrAmbiguousTopic if the subject matches multiple patterns.
func Parse(topic string) (Topic, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, fmt.Errorf("%w: empty topic", ErrInvalidTopic)
	}

	// Check ALL patterns for matches to detect ambiguity
	type match struct {
		pattern topicPattern
		fields  map[string]string
	}
	var matches []match

	for _, pattern := range topicPatterns {
		if fields, ok := matchesPattern(topic, pattern); ok {
			matches = append(matches, match{pattern, fields})
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTopicType, topic)
	}

	if len(matches) > 1 {
		// Build list of matching types for error message
		types := make([]string, len(matches))
		for i, m := range matches {
			types[i] = fmt.Sprintf("%v", m.pattern.typ)
		}
		return nil, fmt.Errorf("%w: topic %q matches types: %s", ErrAmbiguousTopic, topic, strings.Join(types, ", "))
	}

	// Single match - convert fields to typed values using pattern metadata
	m := matches[0]
	stringFields := m.fields
	typedFields := make(map[string]any)

	// Convert each field from string to its proper type based on the pattern
	for _, token := range m.pattern.tokens {
		if token.isLiteral {
			continue // Skip literal tokens
		}

		fieldValue, exists := stringFields[token.field]
		if !exists {
			continue // Field may not exist if it was after a '>' wildcard
		}

		switch token.fieldType {
		case fieldTypeString:
			typedFields[token.field] = fieldValue

		case fieldTypeUUID:
			parsed, err := parseUUIDOrWildcard(fieldValue)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid %s UUID: %w", ErrInvalidUUID, token.field, err)
			}
			typedFields[token.field] = parsed

		default:
			return nil, fmt.Errorf("%w: %s", ErrUnknownFieldType, token.field)
		}
	}

	// Use factory to construct the topic
	factory, exists := topicFactories[m.pattern.typ]
	if !exists {
		return nil, fmt.Errorf("%w: %v", ErrUnknownTopicType, m.pattern.typ)
	}

	return factory(typedFields)
}

// MustParse is like Parse but panics on error.
func MustParse(topic string) Topic {
	t, err := Parse(topic)
	if err != nil {
		panic(err)
	}
	return t
}

// ParseTenantFromTopic parses a topic and extracts tenant IDs.
// For topics with explicit tenant UUIDs, returns a single-element slice.
// For topics with wildcard tenants (*), it extracts tenant IDs from the context.
// Returns (tenantIDs, topicTypeName, error).
func ParseTenantFromTopic(ctx context.Context, topic string) ([]uuid.UUID, string, error) {
	parsed, err := Parse(topic)
	if err != nil {
		return nil, "", err
	}

	// Extract tenant ID based on topic type
	var tenantID uuid.UUID
	typeName := parsed.Type().String()

	if t, ok := parsed.(MutationEventTopic); ok {
		tenantID = t.GetTenantID()
	} else {
		return nil, typeName, fmt.Errorf("%w: topic type %s does not contain tenant information", ErrInvalidTopic, typeName)
	}

	// If wildcard, extract from context
	if tenantID == uuid.Nil {
		return nil, typeName, fmt.Errorf("%w: wildcard tenant IDs not yet supported", ErrInvalidTopic)
	}

	return []uuid.UUID{tenantID}, typeName, nil
}

// IsValidSubscriptionSubject validates that a subject is suitable for subscriptions.
// Subscription subjects can contain wildcards ('*' for single token, '>' for multiple tokens).
// Based on NATS subject rules:
//   - Subject cannot be empty
//   - No null bytes allowed
//   - No consecutive dots (empty tokens)
//   - No whitespace in tokens
//   - Single wildcard '*' matches exactly one token
//   - Full wildcard '>' must be the last token and matches one or more tokens
func IsValidSubscriptionSubject(subject string) bool {
	if subject == "" {
		return false
	}

	// Check for null bytes
	if strings.Contains(subject, "\x00") {
		return false
	}

	// Split by dots and validate each token
	tokens := strings.Split(subject, ".")
	sawFullWildcard := false

	for i, token := range tokens {
		if token == "" {
			return false // No consecutive dots or leading/trailing dots
		}

		// If we already saw a full wildcard, no more tokens allowed
		if sawFullWildcard {
			return false
		}

		// Check for whitespace
		if strings.ContainsAny(token, " \t\n\r\f") {
			return false
		}

		// Handle wildcards
		if token == ">" {
			// Full wildcard must be the last token
			if i != len(tokens)-1 {
				return false
			}
			sawFullWildcard = true
		} else if token == "*" {
			// Single wildcard is always valid in its position
			continue
		} else if strings.ContainsAny(token, "*>") {
			// Wildcards must be standalone tokens, not mixed with other characters
			return false
		}
	}

	return true
}

// Topic Type Implementations

// Core interfaces for different capabilities
type StreamProvider interface {
	GetStreamName() string
}

type TenantProvider interface {
	GetTenantID() uuid.UUID
	SetTenantID(tenantID uuid.UUID)
}

type EntityProvider interface {
	GetServiceName() string
	GetSchemaName() string
	GetEntityID() uuid.UUID
	GetOperationName() string
}

// Shared utility functions for common logic
func getStreamName(streamName string) string {
	if streamName == "" {
		return DefaultStreamName
	}
	return streamName
}

func formatTopic(tokens ...any) string {
	parts := make([]string, len(tokens))
	for i, token := range tokens {
		parts[i] = formatTopicPart(token)
	}
	return strings.Join(parts, ".")
}

func matchesString(pattern, value string) bool {
	return pattern == "" || pattern == "*" || pattern == value
}

func matchesUUID(pattern, value uuid.UUID) bool {
	return pattern == uuid.Nil || pattern == value
}

// Helper functions that work with interfaces
func matchesStream(a, b StreamProvider) bool {
	aStream := a.GetStreamName()
	bStream := b.GetStreamName()
	return aStream == "" || aStream == "*" || aStream == bStream
}

func matchesTenant(a, b TenantProvider) bool {
	aTenant := a.GetTenantID()
	bTenant := b.GetTenantID()
	return aTenant == uuid.Nil || aTenant == bTenant
}

// CustomEventTopic represents a custom event topic: stream.custom-events
type CustomEventTopic struct {
	StreamName string
}

func (t CustomEventTopic) GetStreamName() string {
	return getStreamName(t.StreamName)
}

func (t CustomEventTopic) String() string {
	return formatTopic(t.GetStreamName(), "custom-events")
}

func (t CustomEventTopic) Type() TopicType {
	return TopicTypeCustomEvent
}

func (t CustomEventTopic) Valid() bool {
	return IsValidSubscriptionSubject(t.String())
}

func (t CustomEventTopic) Matches(other Topic) bool {
	var otherTopic CustomEventTopic

	switch other := other.(type) {
	case CustomEventTopic:
		otherTopic = other
	case *CustomEventTopic:
		otherTopic = *other
	default:
		return false
	}

	return matchesStream(t, otherTopic)
}

// MutationEventTopic represents a CRUD mutation event.
type MutationEventTopic struct {
	StreamName    string
	TenantID      uuid.UUID
	ServiceName   string
	SchemaName    string
	EntityID      uuid.UUID
	OperationName string
}

func (t MutationEventTopic) GetStreamName() string {
	return getStreamName(t.StreamName)
}

func (t MutationEventTopic) GetTenantID() uuid.UUID {
	return t.TenantID
}

func (t *MutationEventTopic) SetTenantID(tenantID uuid.UUID) {
	t.TenantID = tenantID
}

func (t MutationEventTopic) GetServiceName() string   { return t.ServiceName }
func (t MutationEventTopic) GetSchemaName() string    { return t.SchemaName }
func (t MutationEventTopic) GetEntityID() uuid.UUID   { return t.EntityID }
func (t MutationEventTopic) GetOperationName() string { return t.OperationName }

func (t MutationEventTopic) String() string {
	return formatTopic(t.GetStreamName(), t.TenantID, "crud", t.ServiceName, t.SchemaName, t.EntityID, t.OperationName)
}

func (t MutationEventTopic) Type() TopicType {
	return TopicTypeMutationEvent
}

func (t MutationEventTopic) Valid() bool {
	return IsValidSubscriptionSubject(t.String())
}

func (t MutationEventTopic) Matches(other Topic) bool {
	var otherTopic MutationEventTopic

	switch other := other.(type) {
	case MutationEventTopic:
		otherTopic = other
	case *MutationEventTopic:
		otherTopic = *other
	default:
		return false
	}

	return matchesStream(t, otherTopic) &&
		matchesTenant(&t, &otherTopic) &&
		matchesString(t.ServiceName, otherTopic.ServiceName) &&
		matchesString(t.SchemaName, otherTopic.SchemaName) &&
		matchesUUID(t.EntityID, otherTopic.EntityID) &&
		matchesString(t.OperationName, otherTopic.OperationName)
}

// MutationEventWithReplyTopic represents a CRUD mutation with request-reply pattern.
type MutationEventWithReplyTopic struct {
	StreamName    string
	TenantID      uuid.UUID
	ServiceName   string
	SchemaName    string
	EntityID      uuid.UUID
	OperationName string
}

func (t MutationEventWithReplyTopic) GetStreamName() string {
	return getStreamName(t.StreamName)
}

func (t MutationEventWithReplyTopic) GetTenantID() uuid.UUID {
	return t.TenantID
}

func (t *MutationEventWithReplyTopic) SetTenantID(tenantID uuid.UUID) {
	t.TenantID = tenantID
}

func (t MutationEventWithReplyTopic) GetServiceName() string   { return t.ServiceName }
func (t MutationEventWithReplyTopic) GetSchemaName() string    { return t.SchemaName }
func (t MutationEventWithReplyTopic) GetEntityID() uuid.UUID   { return t.EntityID }
func (t MutationEventWithReplyTopic) GetOperationName() string { return t.OperationName }

func (t MutationEventWithReplyTopic) String() string {
	return formatTopic("request", "reply", t.GetStreamName(), t.TenantID, "crud", t.ServiceName, t.SchemaName, t.EntityID, t.OperationName)
}

func (t MutationEventWithReplyTopic) Type() TopicType {
	return TopicTypeMutationEventWithReply
}

func (t MutationEventWithReplyTopic) Valid() bool {
	return IsValidSubscriptionSubject(t.String())
}

func (t MutationEventWithReplyTopic) Matches(other Topic) bool {
	var otherTopic MutationEventWithReplyTopic

	switch other := other.(type) {
	case MutationEventWithReplyTopic:
		otherTopic = other
	case *MutationEventWithReplyTopic:
		otherTopic = *other
	default:
		return false
	}

	return matchesStream(t, otherTopic) &&
		matchesTenant(&t, &otherTopic) &&
		matchesString(t.ServiceName, otherTopic.ServiceName) &&
		matchesString(t.SchemaName, otherTopic.SchemaName) &&
		matchesUUID(t.EntityID, otherTopic.EntityID) &&
		matchesString(t.OperationName, otherTopic.OperationName)
}

// DeadLetterEventTopic represents a CRUD mutation event that exhausted its
// publish retries and was dead-lettered to the DLQ stream. It mirrors
// MutationEventTopic but uses the "dlq" segment instead of "crud", so consumers
// can subscribe to dead-lettered events with the same wildcard schema.
type DeadLetterEventTopic struct {
	StreamName    string
	TenantID      uuid.UUID
	ServiceName   string
	SchemaName    string
	EntityID      uuid.UUID
	OperationName string
}

func (t DeadLetterEventTopic) GetStreamName() string {
	return getStreamName(t.StreamName)
}

func (t DeadLetterEventTopic) GetTenantID() uuid.UUID { return t.TenantID }

func (t *DeadLetterEventTopic) SetTenantID(tenantID uuid.UUID) { t.TenantID = tenantID }

func (t DeadLetterEventTopic) GetServiceName() string   { return t.ServiceName }
func (t DeadLetterEventTopic) GetSchemaName() string    { return t.SchemaName }
func (t DeadLetterEventTopic) GetEntityID() uuid.UUID   { return t.EntityID }
func (t DeadLetterEventTopic) GetOperationName() string { return t.OperationName }

func (t DeadLetterEventTopic) String() string {
	return formatTopic(t.GetStreamName(), t.TenantID, "dlq", t.ServiceName, t.SchemaName, t.EntityID, t.OperationName)
}

func (t DeadLetterEventTopic) Type() TopicType { return TopicTypeDeadLetterEvent }

func (t DeadLetterEventTopic) Valid() bool {
	return IsValidSubscriptionSubject(t.String())
}

func (t DeadLetterEventTopic) Matches(other Topic) bool {
	var otherTopic DeadLetterEventTopic

	switch other := other.(type) {
	case DeadLetterEventTopic:
		otherTopic = other
	case *DeadLetterEventTopic:
		otherTopic = *other
	default:
		return false
	}

	return matchesStream(t, otherTopic) &&
		matchesTenant(&t, &otherTopic) &&
		matchesString(t.ServiceName, otherTopic.ServiceName) &&
		matchesString(t.SchemaName, otherTopic.SchemaName) &&
		matchesUUID(t.EntityID, otherTopic.EntityID) &&
		matchesString(t.OperationName, otherTopic.OperationName)
}

// UpdateEventTopic represents an entity attribute update event.
type UpdateEventTopic struct {
	StreamName    string
	TenantID      uuid.UUID
	ServiceName   string
	SchemaName    string
	EntityID      uuid.UUID
	OperationName string
	AttributeName string
}

func (t UpdateEventTopic) GetStreamName() string {
	return getStreamName(t.StreamName)
}

func (t UpdateEventTopic) GetTenantID() uuid.UUID {
	return t.TenantID
}

func (t *UpdateEventTopic) SetTenantID(tenantID uuid.UUID) {
	t.TenantID = tenantID
}

func (t UpdateEventTopic) GetServiceName() string   { return t.ServiceName }
func (t UpdateEventTopic) GetSchemaName() string    { return t.SchemaName }
func (t UpdateEventTopic) GetEntityID() uuid.UUID   { return t.EntityID }
func (t UpdateEventTopic) GetOperationName() string { return t.OperationName }

func (t UpdateEventTopic) String() string {
	return formatTopic(t.GetStreamName(), t.TenantID, "crud", t.ServiceName, t.SchemaName, t.EntityID, t.OperationName, t.AttributeName)
}

func (t UpdateEventTopic) Type() TopicType {
	return TopicTypeUpdateEvent
}

func (t UpdateEventTopic) Valid() bool {
	return IsValidSubscriptionSubject(t.String())
}

func (t UpdateEventTopic) Matches(other Topic) bool {
	var otherTopic UpdateEventTopic

	switch other := other.(type) {
	case UpdateEventTopic:
		otherTopic = other
	case *UpdateEventTopic:
		otherTopic = *other
	default:
		return false
	}

	return matchesStream(t, otherTopic) &&
		matchesTenant(&t, &otherTopic) &&
		matchesString(t.ServiceName, otherTopic.ServiceName) &&
		matchesString(t.SchemaName, otherTopic.SchemaName) &&
		matchesUUID(t.EntityID, otherTopic.EntityID) &&
		matchesString(t.OperationName, otherTopic.OperationName) &&
		matchesString(t.AttributeName, otherTopic.AttributeName)
}

// WorkflowEventTopic represents a workflow-related event.
type WorkflowEventTopic struct {
	StreamName   string
	TenantID     uuid.UUID
	WorkflowID   uuid.UUID
	WorkflowName string
}

func (t WorkflowEventTopic) GetStreamName() string {
	return getStreamName(t.StreamName)
}

func (t WorkflowEventTopic) GetTenantID() uuid.UUID {
	return t.TenantID
}

func (t *WorkflowEventTopic) SetTenantID(tenantID uuid.UUID) {
	t.TenantID = tenantID
}

func (t WorkflowEventTopic) String() string {
	return formatTopic(t.GetStreamName(), t.TenantID, "workflows", t.WorkflowID, t.WorkflowName)
}

func (t WorkflowEventTopic) Type() TopicType {
	return TopicTypeWorkflowEvent
}

func (t WorkflowEventTopic) Valid() bool {
	return IsValidSubscriptionSubject(t.String())
}

func (t WorkflowEventTopic) Matches(other Topic) bool {
	var otherTopic WorkflowEventTopic

	switch other := other.(type) {
	case WorkflowEventTopic:
		otherTopic = other
	case *WorkflowEventTopic:
		otherTopic = *other
	default:
		return false
	}

	return matchesStream(t, otherTopic) &&
		matchesTenant(&t, &otherTopic) &&
		matchesUUID(t.WorkflowID, otherTopic.WorkflowID) &&
		matchesString(t.WorkflowName, otherTopic.WorkflowName)
}

// TemporalWorkflowStateChangeTopic represents a temporal workflow-related event.
type TemporalWorkflowStateChangeTopic struct {
	StreamName       string
	Namespace        string
	TaskQueue        string
	WorkflowTypeName string
	WorkflowID       string
	RunID            string
	Status           string
}

func (t TemporalWorkflowStateChangeTopic) GetStreamName() string {
	return getStreamName(t.StreamName)
}

func (t TemporalWorkflowStateChangeTopic) GetNamespace() string {
	return t.Namespace
}

func (t TemporalWorkflowStateChangeTopic) GetTaskQueue() string {
	return t.TaskQueue
}

func (t TemporalWorkflowStateChangeTopic) GetWorkflowTypeName() string {
	return t.WorkflowTypeName
}

func (t TemporalWorkflowStateChangeTopic) GetWorkflowID() string {
	return t.WorkflowID
}

func (t TemporalWorkflowStateChangeTopic) GetRunID() string {
	return t.RunID
}

func (t TemporalWorkflowStateChangeTopic) GetStatus() string {
	return t.Status
}

func (t TemporalWorkflowStateChangeTopic) GetTenantID() uuid.UUID {
	ns := t.GetNamespace()

	if ns == "" || ns == "*" {
		return uuid.Nil
	}

	tenantID, err := uuid.Parse(ns)
	if err != nil {
		panic(fmt.Sprintf("invalid namespace UUID in topic: %v", err))
	}

	return tenantID
}

func (t *TemporalWorkflowStateChangeTopic) SetTenantID(tenantID uuid.UUID) {
	t.Namespace = tenantID.String()
}

func (t TemporalWorkflowStateChangeTopic) String() string {
	return formatTopic(t.GetStreamName(), t.GetNamespace(), "temporal", t.GetTaskQueue(), t.GetWorkflowTypeName(), t.GetWorkflowID(), t.GetRunID(), t.GetStatus())
}

func (t TemporalWorkflowStateChangeTopic) Type() TopicType {
	return TopicTypeTemporalWorkflowStateChangeEvent
}

func (t TemporalWorkflowStateChangeTopic) Valid() bool {
	return IsValidSubscriptionSubject(t.String())
}

func (t TemporalWorkflowStateChangeTopic) Matches(other Topic) bool {
	var otherTopic TemporalWorkflowStateChangeTopic

	switch other := other.(type) {
	case TemporalWorkflowStateChangeTopic:
		otherTopic = other
	case *TemporalWorkflowStateChangeTopic:
		otherTopic = *other
	default:
		return false
	}

	return matchesStream(t, otherTopic) &&
		matchesString(t.Namespace, otherTopic.Namespace) &&
		matchesString(t.TaskQueue, otherTopic.TaskQueue) &&
		matchesString(t.WorkflowTypeName, otherTopic.WorkflowTypeName) &&
		matchesString(t.WorkflowID, otherTopic.WorkflowID) &&
		matchesString(t.RunID, otherTopic.RunID) &&
		matchesString(t.Status, otherTopic.Status)
}

// fieldType indicates the data type of a topic field.
type fieldType int

const (
	fieldTypeString fieldType = iota
	fieldTypeUUID
)

// topicToken represents a single token in a topic pattern.
type topicToken struct {
	field     string    // Field name (e.g., "stream", "tenant", "crud")
	isLiteral bool      // true if this must match exactly, false if it's a variable
	fieldType fieldType // data type for variable fields (ignored for literals)
}

// topicFactory is a function that constructs a Topic from parsed fields.
type topicFactory func(fields map[string]any) (Topic, error)

// fieldBuilder provides a fluent API for extracting typed fields from a map
// with error accumulation. Once an error occurs, all subsequent extraction
// methods become no-ops, returning zero values. This allows for cleaner
// factory code with a single error check at the end.
type fieldBuilder struct {
	fields map[string]any
	err    error
}

// newFieldBuilder creates a new field builder for the given fields map.
func newFieldBuilder(fields map[string]any) *fieldBuilder {
	return &fieldBuilder{fields: fields}
}

// getString extracts a string field. If an error has already occurred or the
// field is not a string, returns empty string and records the error.
func (b *fieldBuilder) getString(key string) string {
	if b.err != nil {
		return ""
	}
	val, ok := b.fields[key].(string)
	if !ok {
		b.err = fmt.Errorf("%w: %s field type mismatch", ErrInvalidTopic, key)
		return ""
	}
	val, err := unescapeTopicToken(val)
	if err != nil {
		b.err = fmt.Errorf("%w: %s field unescape error: %w", ErrInvalidTopic, key, err)
		return ""
	}
	return val
}

// getUUID extracts a UUID field. If an error has already occurred or the
// field is not a UUID, returns uuid.Nil and records the error.
func (b *fieldBuilder) getUUID(key string) uuid.UUID {
	if b.err != nil {
		return uuid.Nil
	}
	val, ok := b.fields[key].(uuid.UUID)
	if !ok {
		b.err = fmt.Errorf("%w: %s field type mismatch", ErrInvalidTopic, key)
		return uuid.Nil
	}
	return val
}

// topicSpec combines the pattern and factory for a topic type.
type topicSpec struct {
	pattern []topicToken
	factory topicFactory
}

// knownTopics defines all known topic types with their patterns and factories.
// Ordered from most to least specific to handle ambiguous matches.
var knownTopics = map[TopicType]topicSpec{
	// CustomEvent: <stream>.custom-events
	TopicTypeCustomEvent: {
		pattern: []topicToken{
			{"stream", false, fieldTypeString},
			{"custom-events", true, fieldTypeString},
		},
		factory: func(fields map[string]any) (Topic, error) {
			b := newFieldBuilder(fields)
			topic := &CustomEventTopic{
				StreamName: b.getString("stream"),
			}
			return topic, b.err
		},
	},

	// MutationEventWithReply: request.reply.<stream>.<tenant>.crud.<service>.<schema>.<entity>.<operation>
	TopicTypeMutationEventWithReply: {
		pattern: []topicToken{
			{"request", true, fieldTypeString},
			{"reply", true, fieldTypeString},
			{"stream", false, fieldTypeString},
			{"tenant", false, fieldTypeUUID},
			{"crud", true, fieldTypeString},
			{"service", false, fieldTypeString},
			{"schema", false, fieldTypeString},
			{"entity", false, fieldTypeUUID},
			{"operation", false, fieldTypeString},
		},
		factory: func(fields map[string]any) (Topic, error) {
			b := newFieldBuilder(fields)
			topic := &MutationEventWithReplyTopic{
				StreamName:    b.getString("stream"),
				TenantID:      b.getUUID("tenant"),
				ServiceName:   b.getString("service"),
				SchemaName:    b.getString("schema"),
				EntityID:      b.getUUID("entity"),
				OperationName: b.getString("operation"),
			}
			return topic, b.err
		},
	},

	// UpdateEvent: <stream>.<tenant>.crud.<service>.<schema>.<entity>.<operation>.<attribute>
	TopicTypeUpdateEvent: {
		pattern: []topicToken{
			{"stream", false, fieldTypeString},
			{"tenant", false, fieldTypeUUID},
			{"crud", true, fieldTypeString},
			{"service", false, fieldTypeString},
			{"schema", false, fieldTypeString},
			{"entity", false, fieldTypeUUID},
			{"operation", false, fieldTypeString},
			{"attribute", false, fieldTypeString},
		},
		factory: func(fields map[string]any) (Topic, error) {
			b := newFieldBuilder(fields)
			topic := &UpdateEventTopic{
				StreamName:    b.getString("stream"),
				TenantID:      b.getUUID("tenant"),
				ServiceName:   b.getString("service"),
				SchemaName:    b.getString("schema"),
				EntityID:      b.getUUID("entity"),
				OperationName: b.getString("operation"),
				AttributeName: b.getString("attribute"),
			}
			return topic, b.err
		},
	},

	// MutationEvent: <stream>.<tenant>.crud.<service>.<schema>.<entity>.<operation>
	TopicTypeMutationEvent: {
		pattern: []topicToken{
			{"stream", false, fieldTypeString},
			{"tenant", false, fieldTypeUUID},
			{"crud", true, fieldTypeString},
			{"service", false, fieldTypeString},
			{"schema", false, fieldTypeString},
			{"entity", false, fieldTypeUUID},
			{"operation", false, fieldTypeString},
		},
		factory: func(fields map[string]any) (Topic, error) {
			b := newFieldBuilder(fields)
			topic := &MutationEventTopic{
				StreamName:    b.getString("stream"),
				TenantID:      b.getUUID("tenant"),
				ServiceName:   b.getString("service"),
				SchemaName:    b.getString("schema"),
				EntityID:      b.getUUID("entity"),
				OperationName: b.getString("operation"),
			}
			return topic, b.err
		},
	},

	// WorkflowEvent: <stream>.<tenant>.workflows.<workflowID>.<workflowName>
	TopicTypeWorkflowEvent: {
		pattern: []topicToken{
			{"stream", false, fieldTypeString},
			{"tenant", false, fieldTypeUUID},
			{"workflows", true, fieldTypeString},
			{"workflowID", false, fieldTypeUUID},
			{"workflowName", false, fieldTypeString},
		},
		factory: func(fields map[string]any) (Topic, error) {
			b := newFieldBuilder(fields)
			topic := &WorkflowEventTopic{
				StreamName:   b.getString("stream"),
				TenantID:     b.getUUID("tenant"),
				WorkflowID:   b.getUUID("workflowID"),
				WorkflowName: b.getString("workflowName"),
			}
			return topic, b.err
		},
	},

	// TemporalWorkflowEvent: <stream>.<namespace>.temporal.<taskQueue>.<workflowTypeName>.<workflowID>.<runID>.<status>
	TopicTypeTemporalWorkflowStateChangeEvent: {
		pattern: []topicToken{
			{"stream", false, fieldTypeString},
			{"namespace", false, fieldTypeString},
			{"temporal", true, fieldTypeString},
			{"taskQueue", false, fieldTypeString},
			{"workflowTypeName", false, fieldTypeString},
			{"workflowID", false, fieldTypeString},
			{"runID", false, fieldTypeString},
			{"status", false, fieldTypeString},
		},
		factory: func(fields map[string]any) (Topic, error) {
			b := newFieldBuilder(fields)
			topic := &TemporalWorkflowStateChangeTopic{
				StreamName:       b.getString("stream"),
				Namespace:        b.getString("namespace"),
				TaskQueue:        b.getString("taskQueue"),
				WorkflowTypeName: b.getString("workflowTypeName"),
				WorkflowID:       b.getString("workflowID"),
				RunID:            b.getString("runID"),
				Status:           b.getString("status"),
			}
			return topic, b.err
		},
	},
}

// topicPatterns is derived from knownTopics for pattern matching.
var topicPatterns = buildTopicPatterns()

// topicFactories is derived from knownTopics for topic construction.
var topicFactories = buildTopicFactories()

// buildTopicPatterns constructs the pattern list from knownTopics.
func buildTopicPatterns() []topicPattern {
	patterns := make([]topicPattern, 0, len(knownTopics))
	for typ, spec := range knownTopics {
		patterns = append(patterns, topicPattern{
			typ:    typ,
			tokens: spec.pattern,
		})
	}
	return patterns
}

// buildTopicFactories constructs the factory map from knownTopics.
func buildTopicFactories() map[TopicType]topicFactory {
	factories := make(map[TopicType]topicFactory, len(knownTopics))
	for typ, spec := range knownTopics {
		factories[typ] = spec.factory
	}
	return factories
}

// topicPattern defines a pattern for matching NATS subjects to topic types.
type topicPattern struct {
	typ    TopicType
	tokens []topicToken
}

// matchesPattern checks if a subject matches a specific topic pattern.
// It supports NATS wildcards: '*' matches one token, '>' matches one or more tokens.
func matchesPattern(subject string, pattern topicPattern) (map[string]string, bool) {
	parts := strings.Split(subject, ".")

	// Check if subject contains '>' wildcard
	fullWildcardIdx := -1
	for i, part := range parts {
		if part == ">" {
			fullWildcardIdx = i
			// '>' must be the last token
			if i != len(parts)-1 {
				return nil, false
			}
			break
		}
	}

	// If we have '>', the pattern must have at least as many tokens as the subject up to '>'
	// Example: "pyck.*.crud.>" (4 parts) needs pattern with >= 4 tokens
	if fullWildcardIdx >= 0 {
		if len(pattern.tokens) < fullWildcardIdx+1 {
			return nil, false
		}
	} else {
		// Without '>', part count must match token count exactly
		if len(parts) != len(pattern.tokens) {
			return nil, false
		}
	}

	fields := make(map[string]string)

	// Match each token in the pattern
	for i, token := range pattern.tokens {
		// If we've passed the '>' wildcard position in the subject
		if fullWildcardIdx >= 0 && i >= fullWildcardIdx {
			// All remaining variable fields get wildcard value
			if !token.isLiteral {
				fields[token.field] = "*"
			}
			continue
		}

		// Make sure we have a part for this position
		if i >= len(parts) {
			return nil, false
		}

		part := parts[i]

		if token.isLiteral {
			// Literal token must match exactly (or be a wildcard)
			if part != token.field && part != "*" && part != ">" {
				return nil, false
			}
			// Don't store literal tokens in fields
		} else {
			// Variable token - store the value
			fields[token.field] = part
		}
	}

	return fields, true
}

// parseUUIDOrWildcard parses a UUID or returns uuid.Nil for wildcard.
func parseUUIDOrWildcard(s string) (uuid.UUID, error) {
	if s == "*" {
		return uuid.Nil, nil
	}
	return uuid.Parse(s)
}

// formatTopicPart converts a value to a topic token, using "*" for empty/nil values.
func formatTopicPart(v any) string {
	switch v := v.(type) {
	case uuid.UUID:
		if v == uuid.Nil {
			return "*"
		}
		return strings.ToLower(v.String())
	case string:
		if v == "" {
			return "*"
		}
		return escapeTopicToken(strings.ToLower(v))
	case fmt.Stringer:
		s := v.String()
		if s == "" {
			return "*"
		}
		return escapeTopicToken(strings.ToLower(s))
	default:
		return "*"
	}
}

// TODO(michael) topic tokens need proper escaping/unescaping to handle special
// characters. Currently it is easily possible to create invalid topics by using
// special characters in the token strings. This is especially important for
// workflow registrations, where names are user-defined and may contain dots or
// other special characters.

func escapeTopicToken(token string) string {
	// TODO: implement proper escaping

	// replace all non-alphanumeric characters with dash for now
	var sb strings.Builder

	for _, r := range strings.ToLower(strings.TrimSpace(token)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	return sb.String()
}

func unescapeTopicToken(token string) (string, error) {
	return token, nil // TODO: implement proper unescaping
}
