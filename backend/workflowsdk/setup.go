package workflowsdk

import (
	"context"
	"sync"

	"github.com/pyck-ai/pyck/backend/workflowsdk/registry"
)

var defaultSetupRegistry SetupRegistry

type SetupFunc func(ctx context.Context, r *registry.Registry) error

type SetupRegistry struct {
	mu     sync.RWMutex
	setups []SetupFunc
}

func (s *SetupRegistry) Register(setup SetupFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.setups = append(s.setups, setup)
}

func (s *SetupRegistry) Items() []SetupFunc {
	s.mu.RLock()
	defer s.mu.RUnlock()

	funcs := make([]SetupFunc, len(s.setups))
	copy(funcs, s.setups)

	return funcs
}

func Setup(funcs ...SetupFunc) {
	for _, f := range funcs {
		defaultSetupRegistry.Register(f)
	}
}
