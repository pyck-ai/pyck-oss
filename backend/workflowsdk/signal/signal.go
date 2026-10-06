package signal

import (
	"github.com/pyck-ai/pyck/backend/common/events"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
)

// Signal represents a named event that a workflow can receive.
// Type indicates whether this signal starts, continues, or ends a workflow.
type Signal struct {
	SignalName string
	SignalType entworkflowsignal.TemporalSignalType
	Topic      events.Topic
	FilterRule string
}

// Clone returns a copy of the signal, safe for concurrent use.
func (s Signal) Clone() Signal {
	return Signal{
		SignalName: s.SignalName,
		SignalType: s.SignalType,
		Topic:      s.Topic,
		FilterRule: s.FilterRule,
	}
}

// NewSignal initialises a signal builder.
func NewSignal(signalType entworkflowsignal.TemporalSignalType, topic events.Topic, options ...SignalOption) *Signal {
	signal := &Signal{
		SignalType: signalType,
		Topic:      topic,
	}
	for _, opt := range options {
		opt(signal)
	}
	return signal
}

// NewStartSignal initialises a start signal builder.
func NewStartSignal(topic events.Topic, options ...SignalOption) *Signal {
	return NewSignal(entworkflowsignal.TemporalSignalTypeStart, topic, options...)
}

// NewIntermediateSignal initialises an intermediate signal builder.
func NewIntermediateSignal(topic events.Topic, signalName string, options ...SignalOption) *Signal {
	s := NewSignal(entworkflowsignal.TemporalSignalTypeIntermediate, topic, options...)
	s.SignalName = signalName
	return s
}

// NewSignalWithStartSignal initialises a Signal-With-Start signal builder. On
// each matching event the router signals the workflow `<workflow>_<entity ID>`
// with signalName, starting it first if it is not running. The first event's
// data is also the start input, so the workflow sees that event twice, as
// input and as signal.
func NewSignalWithStartSignal(topic events.Topic, signalName string, options ...SignalOption) *Signal {
	s := NewSignal(entworkflowsignal.TemporalSignalTypeSignalWithStart, topic, options...)
	s.SignalName = signalName
	return s
}

// NewSignalByIDSignal initialises a signal-by-ID signal builder. On each
// matching event the router signals the workflow `<workflow>_<entity ID>` with
// signalName and never starts anything: with no running execution the event is
// dropped and recorded as such.
func NewSignalByIDSignal(topic events.Topic, signalName string, options ...SignalOption) *Signal {
	s := NewSignal(entworkflowsignal.TemporalSignalTypeSignalByID, topic, options...)
	s.SignalName = signalName
	return s
}

type SignalOption func(*Signal)

func WithFilterRule(rule string) SignalOption {
	return func(s *Signal) {
		s.FilterRule = rule
	}
}

// IsStart reports whether the signal acts as the workflow entry point.
func (s Signal) IsStart() bool {
	return s.SignalType == entworkflowsignal.TemporalSignalTypeStart
}

// IsIntermediate reports whether the signal is used while the workflow is running.
func (s Signal) IsIntermediate() bool {
	return s.SignalType == entworkflowsignal.TemporalSignalTypeIntermediate
}

// IsSignalWithStart reports whether the signal starts the workflow if needed and signals it.
func (s Signal) IsSignalWithStart() bool {
	return s.SignalType == entworkflowsignal.TemporalSignalTypeSignalWithStart
}

// IsSignalByID reports whether the signal goes to the workflow of the event's ID and never starts it.
func (s Signal) IsSignalByID() bool {
	return s.SignalType == entworkflowsignal.TemporalSignalTypeSignalByID
}
