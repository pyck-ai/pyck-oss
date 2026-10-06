package resolvers

import (
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
)

// signalTypeHasName reports whether a subscription kind delivers a named
// signal, so that its name is part of the subscription's identity: everything
// but a plain start.
func signalTypeHasName(t entworkflowsignal.TemporalSignalType) bool {
	switch t {
	case entworkflowsignal.TemporalSignalTypeIntermediate,
		entworkflowsignal.TemporalSignalTypeSignalWithStart,
		entworkflowsignal.TemporalSignalTypeSignalByID:
		return true
	case entworkflowsignal.TemporalSignalTypeUnknown, entworkflowsignal.TemporalSignalTypeStart:
		return false
	}

	return false
}
