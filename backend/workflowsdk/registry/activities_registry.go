package registry

import (
	"fmt"
	"reflect"
	"sync"
)

type ActivityRegistry struct {
	mu         sync.RWMutex
	activities map[string]map[string]ActivityRegistryEntry
}

func (a *ActivityRegistry) Register(taskQueue string, activity any) error {
	// ensure activity is a struct pointer
	if ptrKind := reflect.TypeOf(activity).Kind(); ptrKind != reflect.Pointer {
		return fmt.Errorf("%w: expected struct pointer, got %s", ErrInvalidActivityType, ptrKind)
	} else if structKind := reflect.TypeOf(activity).Elem().Kind(); structKind != reflect.Struct {
		return fmt.Errorf("%w: expected struct pointer, got %s", ErrInvalidActivityType, structKind)
	}

	typeElem := reflect.TypeOf(activity).Elem()
	typeName := fmt.Sprintf("%s.%s", typeElem.PkgPath(), typeElem.Name())

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.activities == nil {
		a.activities = make(map[string]map[string]ActivityRegistryEntry)
	}

	if a.activities[taskQueue] == nil {
		a.activities[taskQueue] = make(map[string]ActivityRegistryEntry)
	} else if _, ok := a.activities[taskQueue][typeName]; ok {
		return fmt.Errorf("%w: activity %q already registered on task queue %q", ErrInvalidActivityType, typeName, taskQueue)
	}

	a.activities[taskQueue][typeName] = ActivityRegistryEntry{
		Activity:  activity,
		Type:      typeName,
		TaskQueue: taskQueue,
	}

	return nil
}

func (a *ActivityRegistry) Items() []ActivityRegistryEntry {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.activities == nil {
		return nil
	}

	var entries []ActivityRegistryEntry
	for taskQueue := range a.activities {
		for _, entry := range a.activities[taskQueue] {
			entries = append(entries, entry)
		}
	}

	return entries
}

type ActivityRegistryEntry struct {
	Activity  any
	Type      string
	TaskQueue string
}
