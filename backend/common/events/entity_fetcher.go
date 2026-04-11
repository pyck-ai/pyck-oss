package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/internal/fieldnames"
)

// Sentinel errors for entity fetcher operations.
var (
	ErrNilEntityClient     = errors.New("entity client is nil")
	ErrUnexpectedGetMethod = errors.New("unexpected Get method signature")
)

// schemaFetcher holds precomputed reflection info for a schema.
type schemaFetcher struct {
	fieldIndex []int         // Index path to the entity client field
	getMethod  reflect.Value // Cached Get method to avoid MethodByName on each call
	entityType reflect.Type  // The entity type returned by Get (for pre-building type info cache)
}

// BuildEntityFetcher creates an EntityFetcher by inspecting the Ent Tx type at startup.
// This eliminates manual maintenance of schema-to-fetcher mappings.
//
// The generic type parameter T allows passing typed txFromContext functions directly
// without wrapping them in a func(context.Context) any closure.
func BuildEntityFetcher[T any](txFromContext func(context.Context) T, dataField fieldnames.FieldName, exclude ...fieldnames.FieldName) func(context.Context, string, uuid.UUID) (any, error) {
	excludeSet := make(map[string]struct{})
	// Always exclude the outbox schema to prevent infinite recursion
	excludeSet[fieldnames.FieldEntityEventsOutbox.String()] = struct{}{}
	for _, s := range exclude {
		excludeSet[s.String()] = struct{}{}
	}

	// Cache for schema fetchers, populated lazily on first access.
	// We can't build the full cache at startup because we don't have a Tx instance yet.
	// sync.Once ensures safe concurrent initialization from multiple goroutines.
	var (
		once     sync.Once
		fetchers map[string]*schemaFetcher
	)

	return func(ctx context.Context, schema string, id uuid.UUID) (any, error) {
		if _, excluded := excludeSet[schema]; excluded {
			return nil, nil
		}

		tx := txFromContext(ctx)
		rv := reflect.ValueOf(tx)
		if !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
			return nil, nil
		}
		if rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}

		// Initialize cache on first call (startup-time reflection)
		once.Do(func() {
			txType := rv.Type()
			fetchers = buildSchemaFetchers(txType, excludeSet)

			// Pre-build field comparison cache for all discovered entity types.
			// This ensures optimal performance from the first request.
			// Only build if dataField is a valid FieldName.
			if dataField.IsAFieldName() {
				dataFieldStr := dataField.String()
				for _, f := range fetchers {
					if f.entityType != nil {
						info := buildTypeInfo(f.entityType, dataFieldStr)
						typeInfoCache.Store(f.entityType, info)
					}
				}
			}
		})

		// Look up cached fetcher info
		fetcher, ok := fetchers[schema]
		if !ok {
			// Schema not found - this is not an error, just skip
			return nil, nil
		}

		// Get the entity client field
		clientField := rv.FieldByIndex(fetcher.fieldIndex)
		if !clientField.IsValid() || clientField.IsNil() {
			return nil, fmt.Errorf("%w: %s", ErrNilEntityClient, schema)
		}

		// Call Get(ctx, id) on the entity client using cached method.
		// The cached method requires the receiver as the first argument.
		results := fetcher.getMethod.Call([]reflect.Value{
			clientField, // Receiver (the entity client)
			reflect.ValueOf(ctx),
			reflect.ValueOf(id),
		})

		if len(results) != 2 {
			return nil, fmt.Errorf("%w: %s", ErrUnexpectedGetMethod, schema)
		}

		// Check error (second return value)
		if !results[1].IsNil() {
			err, _ := results[1].Interface().(error)
			return nil, err
		}

		return results[0].Interface(), nil
	}
}

// buildSchemaFetchers inspects the Tx type and builds a map of schema -> fetcher info.
// This runs once at startup (on first call).
func buildSchemaFetchers(txType reflect.Type, exclude map[string]struct{}) map[string]*schemaFetcher {
	fetchers := make(map[string]*schemaFetcher)

	for i := range txType.NumField() {
		field := txType.Field(i)

		// Skip non-exported fields
		if !field.IsExported() {
			continue
		}

		// Skip excluded schemas
		if _, excluded := exclude[field.Name]; excluded {
			continue
		}

		// Check if this looks like an entity client (pointer to *XxxClient)
		fieldType := field.Type
		if fieldType.Kind() != reflect.Pointer {
			continue
		}

		typeName := fieldType.Elem().Name()
		if !strings.HasSuffix(typeName, "Client") {
			continue
		}

		// Verify it has a Get method with the expected signature
		getMethod, ok := fieldType.MethodByName("Get")
		if !ok {
			continue
		}

		// Expected signature: Get(context.Context, uuid.UUID) (*Entity, error)
		methodType := getMethod.Type
		if methodType.NumIn() != 3 || methodType.NumOut() != 2 {
			// 3 inputs: receiver, context, uuid; 2 outputs: entity, error
			continue
		}

		// Extract entity type from Get method return type: (*Entity, error)
		// Out(0) is *Entity, Elem() gives us Entity
		entityPtrType := methodType.Out(0)
		var entityType reflect.Type
		if entityPtrType.Kind() == reflect.Pointer {
			entityType = entityPtrType.Elem()
		}

		fetchers[field.Name] = &schemaFetcher{
			fieldIndex: field.Index,
			getMethod:  getMethod.Func, // Cache the method to avoid MethodByName on each call
			entityType: entityType,     // For pre-building field comparison cache
		}
	}

	return fetchers
}
