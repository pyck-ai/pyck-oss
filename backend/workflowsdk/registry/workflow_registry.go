package registry

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/workflowsdk/signal"
)

type WorkflowRegistry struct {
	mu sync.RWMutex

	workflows map[string]WorkflowRegistryEntry
}

func (w *WorkflowRegistry) Register(workflow any, opts ...WorkflowRegistryOption) (string, error) {
	// ensure workflow is a function pointer with correct signature
	if reflect.TypeOf(workflow).Kind() != reflect.Func {
		return "", fmt.Errorf("%w: expected function, got %s", ErrInvalidWorkflowType, reflect.TypeOf(workflow).Kind())
	}

	// derive workflow name from function name
	var (
		wfFuncPtr = reflect.ValueOf(workflow).Pointer()
		wfFunc    = runtime.FuncForPC(wfFuncPtr)
	)

	// create registry entry
	var wf WorkflowRegistryEntry

	for _, opt := range opts {
		opt(&wf)
	}

	wf.Workflow = workflow

	if wf.RegisterOptions.Name == "" {
		if parts := strings.Split(wfFunc.Name(), "."); len(parts) > 0 {
			wf.RegisterOptions.Name = parts[len(parts)-1]
		} else {
			wf.RegisterOptions.Name = wfFunc.Name()
		}
	}

	if wf.StartOptions.TaskQueue == "" {
		wf.StartOptions.TaskQueue = strings.ToLower(wf.Type())
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.workflows == nil {
		w.workflows = make(map[string]WorkflowRegistryEntry)
	}

	// Check if workflow with this name is already registered
	if existing, exists := w.workflows[wf.Type()]; exists {
		return "", fmt.Errorf("%w: workflow %q already registered (existing: %v, new: %v)",
			ErrWorkflowAlreadyRegistered,
			wf.Type(),
			runtime.FuncForPC(reflect.ValueOf(existing.Workflow).Pointer()).Name(),
			wfFunc.Name())
	}

	w.workflows[wf.Type()] = wf

	return wf.TaskQueue(), nil
}

func (w *WorkflowRegistry) Items() []WorkflowRegistryEntry {
	w.mu.RLock()
	defer w.mu.RUnlock()

	entries := make([]WorkflowRegistryEntry, 0, len(w.workflows))

	for _, entry := range w.workflows {
		entries = append(entries, entry)
	}

	return entries
}

type WorkflowRegistryEntry struct {
	Workflow any

	Signals []signal.Signal

	Data         map[string]any
	DataTypeID   uuid.UUID
	DataTypeSlug string

	RegisterOptions workflow.RegisterOptions
	StartOptions    client.StartWorkflowOptions
	ActivityOptions workflow.ActivityOptions
}

func (e *WorkflowRegistryEntry) Type() string {
	return e.RegisterOptions.Name
}

func (e *WorkflowRegistryEntry) TaskQueue() string {
	return e.StartOptions.TaskQueue
}

type WorkflowRegistryOption func(*WorkflowRegistryEntry)

func WithWorkflowSignals(signals ...*signal.Signal) WorkflowRegistryOption {
	return func(w *WorkflowRegistryEntry) {
		for _, signal := range signals {
			if signal == nil {
				continue
			}

			w.Signals = append(w.Signals, *signal)
		}
	}
}

func WithWorkflowData(dataTypeID uuid.UUID, dataTypeSlug string, data map[string]any) WorkflowRegistryOption {
	return func(w *WorkflowRegistryEntry) {
		w.DataTypeID = dataTypeID
		w.DataTypeSlug = dataTypeSlug
		w.Data = data
	}
}

func WithWorkflowRegisterOptions(options workflow.RegisterOptions) WorkflowRegistryOption {
	return func(w *WorkflowRegistryEntry) {
		w.RegisterOptions = options
	}
}

func WithWorkflowStartOptions(options client.StartWorkflowOptions) WorkflowRegistryOption {
	return func(w *WorkflowRegistryEntry) {
		w.StartOptions = options
	}
}
