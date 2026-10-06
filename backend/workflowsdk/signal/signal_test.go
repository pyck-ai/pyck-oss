package signal_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/events"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"

	"github.com/pyck-ai/pyck/backend/workflowsdk/signal"
)

func TestSignalConstructors(t *testing.T) {
	t.Parallel()

	topic := events.MutationEventTopic{ServiceName: "inventory", SchemaName: "item", OperationName: "update"}

	cases := map[string]struct {
		s       *signal.Signal
		typ     entworkflowsignal.TemporalSignalType
		name    string
		is      func(signal.Signal) bool
		notIsOf []func(signal.Signal) bool
	}{
		"start": {
			s: signal.NewStartSignal(topic), typ: entworkflowsignal.TemporalSignalTypeStart,
			is: signal.Signal.IsStart,
		},
		"intermediate": {
			s: signal.NewIntermediateSignal(topic, "X"), typ: entworkflowsignal.TemporalSignalTypeIntermediate, name: "X",
			is: signal.Signal.IsIntermediate,
		},
		"signal with start": {
			s: signal.NewSignalWithStartSignal(topic, "X", signal.WithFilterRule("true")), typ: entworkflowsignal.TemporalSignalTypeSignalWithStart, name: "X",
			is: signal.Signal.IsSignalWithStart,
		},
		"signal by id": {
			s: signal.NewSignalByIDSignal(topic, "X"), typ: entworkflowsignal.TemporalSignalTypeSignalByID, name: "X",
			is: signal.Signal.IsSignalByID,
		},
	}

	predicates := []func(signal.Signal) bool{
		signal.Signal.IsStart, signal.Signal.IsIntermediate, signal.Signal.IsSignalWithStart, signal.Signal.IsSignalByID,
	}

	for name, c := range cases {
		assert.Equal(t, c.typ, c.s.SignalType, name)
		assert.Equal(t, c.name, c.s.SignalName, name)
		assert.True(t, c.is(*c.s), name)

		trues := 0

		for _, p := range predicates {
			if p(*c.s) {
				trues++
			}
		}

		assert.Equal(t, 1, trues, "%s: exactly one kind predicate holds", name)
	}

	assert.Equal(t, "true", signal.NewSignalWithStartSignal(topic, "X", signal.WithFilterRule("true")).FilterRule)
}
