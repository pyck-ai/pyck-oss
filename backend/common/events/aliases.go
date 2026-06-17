package events

import (
	"github.com/pyck-ai/pyck/backend/common/events/topic"
)

// This file re-exports the leaf topic package's surface so existing callers of
// the events package continue to compile after the topic split. The leaf was
// extracted so authn (and any other package that would otherwise pull the full
// events graph and trip an import cycle through request) can build subject
// strings via topic.MutationEventTopic{...}.String() and decode payloads into
// topic.MutationEventMessage directly.

// Re-exported constants: stream name, CRUD operation tokens, and the
// TopicType enum values. The underlying types are aliased below.
const (
	DefaultStreamName = topic.DefaultStreamName

	OpCreate = topic.OpCreate
	OpUpdate = topic.OpUpdate
	OpDelete = topic.OpDelete

	TopicTypeUnknown                          = topic.TopicTypeUnknown
	TopicTypeCustomEvent                      = topic.TopicTypeCustomEvent
	TopicTypeMutationEvent                    = topic.TopicTypeMutationEvent
	TopicTypeMutationEventWithReply           = topic.TopicTypeMutationEventWithReply
	TopicTypeUpdateEvent                      = topic.TopicTypeUpdateEvent
	TopicTypeWorkflowEvent                    = topic.TopicTypeWorkflowEvent
	TopicTypeTemporalWorkflowStateChangeEvent = topic.TopicTypeTemporalWorkflowStateChangeEvent
	TopicTypeDeadLetterEvent                  = topic.TopicTypeDeadLetterEvent
)

type (
	Topic     = topic.Topic
	TopicType = topic.TopicType

	StreamProvider = topic.StreamProvider
	TenantProvider = topic.TenantProvider
	EntityProvider = topic.EntityProvider

	CustomEventTopic                 = topic.CustomEventTopic
	MutationEventTopic               = topic.MutationEventTopic
	MutationEventWithReplyTopic      = topic.MutationEventWithReplyTopic
	UpdateEventTopic                 = topic.UpdateEventTopic
	WorkflowEventTopic               = topic.WorkflowEventTopic
	TemporalWorkflowStateChangeTopic = topic.TemporalWorkflowStateChangeTopic
	DeadLetterEventTopic             = topic.DeadLetterEventTopic

	MutationEventMessage = topic.MutationEventMessage
)

// Topic parsing + validation. Function-valued vars so callers of
// events.Parse(...) etc. keep working without source changes.
var (
	Validate                   = topic.Validate
	Parse                      = topic.Parse
	MustParse                  = topic.MustParse
	ParseTenantFromTopic       = topic.ParseTenantFromTopic
	IsValidSubscriptionSubject = topic.IsValidSubscriptionSubject
)

// Errors re-exported as vars (errors are values; errors.Is works through the
// alias since both names point at the same sentinel).
var (
	ErrInvalidTopic     = topic.ErrInvalidTopic
	ErrUnknownTopicType = topic.ErrUnknownTopicType
	ErrInvalidUUID      = topic.ErrInvalidUUID
	ErrAmbiguousTopic   = topic.ErrAmbiguousTopic
	ErrUnknownFieldType = topic.ErrUnknownFieldType
)
