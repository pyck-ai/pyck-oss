package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"time"

	"entgo.io/ent"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/internal/fieldnames"
	"github.com/pyck-ai/pyck/backend/common/internal/searchattributes"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
)

// Sentinel errors for hook operations.
var (
	ErrExtractEntityID  = errors.New("extract entity ID")
	ErrInsertOutbox     = errors.New("insert outbox entry")
	ErrUnknownOperation = errors.New("unknown mutation operation")
	ErrNotStruct        = errors.New("parameters must be structs")
	ErrTypeMismatch     = errors.New("parameters must be of the same type")
	ErrNoTransaction    = errors.New("no transaction in context")
	ErrTypeNotInCache   = errors.New("type not in cache: entity discovery missed this type")
)

// HookConfig configures the MutationEventHook behavior.
type HookConfig struct {
	// Service name (e.g., "inventory", "management")
	Service string

	// StreamName for NATS topics (default: "pyck")
	StreamName string

	// EntityFetcher is called to fetch the current entity state before Update/Delete operations.
	// It receives the context and entity ID, and should return the entity or nil if not found.
	// This is service-specific because each service has different entity types.
	EntityFetcher func(ctx context.Context, schema string, id uuid.UUID) (any, error)

	// OutboxInserter is called to insert an entry into the outbox table.
	// This allows services to use their Ent client to insert within the same transaction.
	// The function receives the context (with active transaction) and the entry to insert.
	OutboxInserter func(ctx context.Context, entry *OutboxEntry) error

	// ExcludedSchemas lists schema names that should not emit events (e.g., "Outbox")
	ExcludedSchemas []string

	// FieldChangeEmitter is called after Update operations to emit field-level change events.
	// The callback receives all necessary info to compare before/after and emit events.
	// The callback should use gqltx.AddPostCommit to schedule event emission after commit.
	// This is optional - if nil, field-level events are not emitted.
	FieldChangeEmitter FieldChangeEmitterFunc
}

// FieldChangeEmitterFunc is the signature for field-level change event callbacks.
// It receives the context, service name, schema, operation, entity ID, tenant ID,
// and the before/after entity states.
type FieldChangeEmitterFunc func(ctx context.Context, info FieldChangeInfo)

// FieldChangeInfo contains all information needed to emit field-level change events.
type FieldChangeInfo struct {
	Service   string
	Schema    string
	Operation string
	EntityID  uuid.UUID
	TenantID  uuid.UUID
	Before    any
	After     any
}

// NewFieldChangeEmitter creates a FieldChangeEmitterFunc that sends field-level change events.
// The addPostCommit function should be gqltx.AddPostCommit to schedule events after transaction commit.
func NewFieldChangeEmitter(publisher Publisher, addPostCommit func(context.Context, func() error)) FieldChangeEmitterFunc {
	return func(ctx context.Context, info FieldChangeInfo) {
		// Build the event message info needed for field-level events
		eventMsg := MutationEventMessage{
			Service:   info.Service,
			Type:      strings.ToLower(info.Service) + info.Schema,
			Schema:    info.Schema,
			Operation: info.Operation,
			ID:        info.EntityID,
			TenantID:  info.TenantID,
		}

		// Capture values for the closure
		before := info.Before
		after := info.After

		// Schedule field-level event emission after commit
		addPostCommit(ctx, func() error {
			return sendFieldChangeEventsAsync(ctx, publisher, eventMsg, before, after)
		})
	}
}

// sendFieldChangeEventsAsync sends field-level change events.
//
// Behavior depends on FEATURE_SYNC_UPDATES feature flag:
//
// SYNC MODE (FEATURE_SYNC_UPDATES enabled - tests/debugging):
//   - Executes synchronously
//   - Returns error immediately if field change events fail
//   - Tests will fail, exposing bugs during development
//
// ASYNC MODE (Production):
//   - Spawns errgroup goroutine for execution
//   - Returns nil immediately (doesn't block caller)
//   - Errors are logged but don't affect main mutation
//   - Context cancellation respected via errgroup
//
// This unified approach using errgroup eliminates the need for panic recovery
// while providing proper error handling in both modes.
func sendFieldChangeEventsAsync(ctx context.Context, publisher Publisher, eventMsg MutationEventMessage, before, after any) error {
	// Always use errgroup - handles both sync and async uniformly
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return sendFieldChangeEvents(gctx, publisher, eventMsg, before, after)
	})

	if feature.HasFeature(ctx, feature.FEATURE_SYNC_UPDATES) {
		// Sync mode: wait for completion and return error (tests will fail)
		return g.Wait()
	}

	// Async mode: spawn goroutine to wait and log errors
	go func() {
		if err := g.Wait(); err != nil {
			log.ForContext(gctx).Error().
				Err(err).
				Msg("error sending field change events")
		}
	}()

	return nil // Return immediately in async mode
}

// sendFieldChangeEvents sends field-level change events synchronously.
func sendFieldChangeEvents(ctx context.Context, publisher Publisher, eventMsg MutationEventMessage, before, after any) error {
	updatedFields, err := getUpdatedFields(before, after)
	if err != nil {
		return err
	}

	for field, change := range updatedFields {
		if err := publisher.SendUpdateEvent(ctx, &UpdateEventMessage{
			Service:   eventMsg.Service,
			Operation: eventMsg.Operation,
			Type:      eventMsg.Type,
			Schema:    eventMsg.Schema,
			ID:        eventMsg.ID,
			TenantID:  eventMsg.TenantID,
			Attribute: strings.ToLower(field),
			Data: UpdateAttributeDetails{
				OldValue: change.oldValue,
				NewValue: change.newValue,
			},
		}); err != nil {
			return err
		}
	}

	return nil
}

// fieldChange represents a changed field with old and new values.
type fieldChange struct {
	oldValue any
	newValue any
}

// structFieldInfo holds cached information about a struct field for comparison.
type structFieldInfo struct {
	name        string
	index       int
	isDataField bool // true if this is the JSON data field (e.g., "Data")
}

// typeInfo holds cached field information for a struct type.
type typeInfo struct {
	fields []structFieldInfo
}

// typeInfoCache stores pre-built field information for entity types.
// Key: reflect.Type, Value: *typeInfo
// This is populated at startup for Ent entities and lazily for other types.
var typeInfoCache sync.Map

// buildTypeInfo builds and caches field information for a struct type.
// This should be called once per type, either at startup or lazily on first use.
func buildTypeInfo(t reflect.Type, dataField string) *typeInfo {
	fields := make([]structFieldInfo, 0, t.NumField())
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		fields = append(fields, structFieldInfo{
			name:        sf.Name,
			index:       i,
			isDataField: dataField != "" && sf.Name == dataField,
		})
	}
	return &typeInfo{fields: fields}
}

// getTypeInfo retrieves cached type info for an entity type.
// The cache is pre-populated by BuildEntityFetcher at startup for all Ent entities.
func getTypeInfo(t reflect.Type) *typeInfo {
	if cached, ok := typeInfoCache.Load(t); ok {
		if info, ok := cached.(*typeInfo); ok {
			return info
		}
	}
	return nil
}

// getUpdatedFields compares two structs and returns changed fields.
// Type info is retrieved from the cache pre-populated by BuildEntityFetcher at startup.
func getUpdatedFields(oldObject, newObject any) (map[string]fieldChange, error) {
	oldVal := reflect.ValueOf(oldObject)
	newVal := reflect.ValueOf(newObject)

	// Dereference pointers
	if oldVal.Kind() == reflect.Pointer {
		if oldVal.IsNil() {
			return map[string]fieldChange{}, nil
		}
		oldVal = oldVal.Elem()
	}
	if newVal.Kind() == reflect.Pointer {
		if newVal.IsNil() {
			return map[string]fieldChange{}, nil
		}
		newVal = newVal.Elem()
	}

	if oldVal.Kind() != reflect.Struct || newVal.Kind() != reflect.Struct {
		return nil, ErrNotStruct
	}

	if oldVal.Type() != newVal.Type() {
		return nil, ErrTypeMismatch
	}

	result := make(map[string]fieldChange)
	t := oldVal.Type()

	// Use cached type info (pre-populated by BuildEntityFetcher at startup)
	info := getTypeInfo(t)
	if info == nil {
		// Type not discovered at startup - this indicates a bug
		return nil, fmt.Errorf("%w: %v", ErrTypeNotInCache, t)
	}

	for _, sf := range info.fields {
		ovField := oldVal.Field(sf.index)
		nvField := newVal.Field(sf.index)

		if !ovField.CanInterface() || !nvField.CanInterface() {
			continue
		}

		// Special handling for JSON data field
		if sf.isDataField {
			oldMap, okOld := ovField.Interface().(map[string]any)
			newMap, okNew := nvField.Interface().(map[string]any)
			if okOld && okNew && !reflect.DeepEqual(oldMap, newMap) {
				result[sf.name] = fieldChange{
					oldValue: getChangedMapValues(newMap, oldMap),
					newValue: getChangedMapValues(oldMap, newMap),
				}
			}
			continue
		}

		oldField := ovField.Interface()
		newField := nvField.Interface()
		if !reflect.DeepEqual(oldField, newField) {
			result[sf.name] = fieldChange{oldValue: oldField, newValue: newField}
		}
	}

	return result, nil
}

// getChangedMapValues returns only the keys that differ between maps.
func getChangedMapValues(oldMap, newMap map[string]any) map[string]any {
	result := make(map[string]any)

	for key, oldValue := range oldMap {
		if newValue, exists := newMap[key]; exists {
			if !reflect.DeepEqual(oldValue, newValue) {
				result[key] = newValue
			}
		} else {
			result[key] = nil
		}
	}

	for key, newValue := range newMap {
		if _, exists := oldMap[key]; !exists {
			result[key] = newValue
		}
	}

	return result
}

// NewMutationEventHook creates a fully configured MutationEventHook with minimal boilerplate.
// This is a convenience wrapper for services following the standard EntityEventsOutbox pattern.
//
// The generic type parameter T is the Ent transaction type (e.g., *ent.Tx).
//
// Usage:
//
//	dbClient.Use(events.NewMutationEventHook(
//	    serviceName,
//	    streamName,
//	    jetstreamPub,
//	    gqltx.AddPostCommit,
//	    ent.TxFromContext,
//	    events.FieldData,
//	))
func NewMutationEventHook[T any](
	serviceName string,
	streamName string,
	publisher Publisher,
	addPostCommit func(context.Context, func() error),
	txFromContext func(context.Context) T,
	dataField fieldnames.FieldName,
) ent.Hook {
	return MutationEventHook(HookConfig{
		Service:            serviceName,
		StreamName:         streamName,
		EntityFetcher:      BuildEntityFetcher(txFromContext, dataField),
		OutboxInserter:     NewEntOutboxInserter(txFromContext),
		FieldChangeEmitter: NewFieldChangeEmitter(publisher, addPostCommit),
	})
}

// MutationEventHook creates an Ent hook that captures mutation events and writes them to the outbox.
//
// The hook:
//   - Captures Create, Update, UpdateOne, Delete, DeleteOne operations
//   - Fetches before-state for Update/Delete operations (via EntityFetcher)
//   - Detects soft deletes (when deleted_at changes from nil to non-nil)
//   - Auto-computes workflow search attributes from entity fields
//   - Merges with context-provided search attributes (via WithExtraSearchAttribute)
//   - Inserts into the outbox table within the same transaction
//
// For a simpler API with standard defaults, use NewMutationEventHook instead.
func MutationEventHook(config HookConfig) ent.Hook {
	if config.StreamName == "" {
		config.StreamName = DefaultStreamName
	}

	excludedSchemas := make(map[string]struct{})
	for _, s := range config.ExcludedSchemas {
		excludedSchemas[s] = struct{}{}
	}
	// Always exclude EntityEventsOutbox to prevent infinite recursion
	excludedSchemas[fieldnames.FieldEntityEventsOutbox.String()] = struct{}{}

	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			schema := extractSchemaName(m)
			if _, excluded := excludedSchemas[schema]; schema == "" || excluded {
				return next.Mutate(ctx, m)
			}

			// Suppress event emission entirely when the feature is set (e.g. the
			// bulk stock rebuild, which replays one write per movement and would
			// otherwise emit ~1 outbox event per replayed row — flooding the
			// outbox and risking OOM when the backlog is drained). Skip the
			// before-state fetch and outbox write (Create/Update/Delete and
			// field-change events alike); just run the mutation.
			//
			// TODO(@george): this feature is currently settable by any API caller
			// via the X-Pyck-Feature header (propagated by the gateway; the
			// feature middleware has no allowlist), which suppresses event
			// emission for arbitrary mutations. This is accepted for now. Access
			// to this feature will be guarded behind RBAC rules in a follow-up.
			if feature.HasFeature(ctx, feature.FEATURE_SUPPRESS_EVENTS) {
				return next.Mutate(ctx, m)
			}

			op, err := determineOperation(m)
			if err != nil {
				return nil, err
			}

			// For Update/Delete: fetch before-state before mutation
			entityID, beforeData, err := prepareBeforeState(ctx, config, m, schema, op)
			if err != nil {
				return nil, err
			}

			// Detect soft delete
			if op == OpUpdate && isSoftDelete(m) {
				op = OpDelete
			}

			// Execute the mutation
			value, err := next.Mutate(ctx, m)
			if err != nil {
				return value, err
			}

			// Bulk operation matched no entities — skip event emission
			if entityID == uuid.Nil && op != OpCreate {
				return value, nil
			}

			// Emit event - failure causes transaction rollback to maintain integrity
			if err := emitOutboxEvent(ctx, config, schema, op, entityID, value, beforeData); err != nil {
				return nil, fmt.Errorf("emit outbox event: %w", err)
			}

			// Emit field-level change events for Update operations (not soft deletes)
			if op == OpUpdate && config.FieldChangeEmitter != nil && beforeData != nil {
				tenantID := extractTenantID(value)
				config.FieldChangeEmitter(ctx, FieldChangeInfo{
					Service:   config.Service,
					Schema:    schema,
					Operation: op,
					EntityID:  entityID,
					TenantID:  tenantID,
					Before:    beforeData,
					After:     value,
				})
			}

			return value, nil
		})
	}
}

// prepareBeforeState fetches the entity ID and before-state for Update/Delete operations.
// Returns zero values for Create operations.
// For bulk operations (OpUpdate/OpDelete, not *One variants) that match zero entities,
// returns uuid.Nil with no error to signal the caller to skip event emission.
func prepareBeforeState(ctx context.Context, config HookConfig, m ent.Mutation, schema string, op string) (uuid.UUID, any, error) {
	if op == OpCreate {
		return uuid.Nil, nil, nil
	}

	entityID, err := extractEntityID(ctx, m)
	if err != nil {
		// Bulk Update/Delete (not *One variants) may match zero rows — this is
		// expected (e.g., clearing defaults on a fresh tenant with no data).
		// Return uuid.Nil to signal the caller to skip event emission.
		if (m.Op() == ent.OpUpdate || m.Op() == ent.OpDelete) && errors.Is(err, ErrExtractEntityID) {
			return uuid.Nil, nil, nil
		}

		return uuid.Nil, nil, fmt.Errorf("extract entity ID: %w", err)
	}

	var beforeData any
	if config.EntityFetcher != nil {
		beforeData, err = config.EntityFetcher(ctx, schema, entityID)
		if err != nil {
			return entityID, nil, fmt.Errorf("fetch before-state: %w", err)
		}
	}

	return entityID, beforeData, nil
}

// emitOutboxEvent builds and inserts an outbox entry for the mutation.
func emitOutboxEvent(ctx context.Context, config HookConfig, schema, op string, entityID uuid.UUID, value ent.Value, beforeData any) error {
	// For Create: extract ID from result
	if op == OpCreate {
		var err error
		entityID, err = extractIDFromValue(value)
		if err != nil {
			return fmt.Errorf("extract ID from created entity: %w", err)
		}
	}

	entry, err := buildOutboxEntry(ctx, config, schema, op, entityID, value, beforeData)
	if err != nil {
		return fmt.Errorf("build outbox entry: %w", err)
	}
	if entry == nil {
		return nil // No outbox entry needed (e.g., system-user mutation without tenant)
	}

	if err := insertOutboxEntry(ctx, config, entry); err != nil {
		return fmt.Errorf("insert outbox entry: %w", err)
	}

	return nil
}

// OutboxEntry represents an entry to be inserted into the outbox table.
// This is exported so services can use it with the OutboxInserter callback.
//
// TransactionID is the canonical dedup key (UUID v7 generated by gqltx at
// BeginTx). TraceID and RequestID are observability-only and may be empty.
type OutboxEntry struct {
	ID            uuid.UUID
	CreatedAt     time.Time
	TransactionID uuid.UUID
	TraceID       string
	RequestID     string
	UserID        *uuid.UUID
	Topic         Topic
	Payload       []byte
	WithReply     bool
	EntityType    *string
	EntityID      *uuid.UUID
	TenantID      uuid.UUID
}

// extractSchemaName extracts the schema name from a mutation.
// Ent mutations have a Type() method that returns something like "Item".
func extractSchemaName(m ent.Mutation) string {
	return m.Type()
}

// determineOperation maps Ent operation to our operation constants.
// Returns ErrUnknownOperation for unrecognized operations.
func determineOperation(m ent.Mutation) (string, error) {
	switch m.Op() {
	case ent.OpCreate:
		return OpCreate, nil
	case ent.OpUpdate, ent.OpUpdateOne:
		return OpUpdate, nil
	case ent.OpDelete, ent.OpDeleteOne:
		return OpDelete, nil
	default:
		return "", fmt.Errorf("%w: %v", ErrUnknownOperation, m.Op())
	}
}

// IDsProvider is an interface for mutations that can provide their IDs.
// Generated Ent mutations implement this interface with []uuid.UUID return type.
type IDsProvider interface {
	IDs(ctx context.Context) ([]uuid.UUID, error)
}

// extractEntityID extracts the entity ID from a mutation.
// For UpdateOne/DeleteOne, uses the mutation's IDs() method.
func extractEntityID(ctx context.Context, m ent.Mutation) (uuid.UUID, error) {
	provider, ok := m.(IDsProvider)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: mutation does not implement IDsProvider", ErrExtractEntityID)
	}

	ids, err := provider.IDs(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %w", ErrExtractEntityID, err)
	}

	if len(ids) == 0 {
		return uuid.Nil, fmt.Errorf("%w: no IDs in mutation", ErrExtractEntityID)
	}

	if len(ids) > 1 {
		return uuid.Nil, fmt.Errorf("%w: batch mutations not supported, got %d IDs", ErrExtractEntityID, len(ids))
	}

	return ids[0], nil
}

// extractIDFromValue extracts the ID from a mutation result value using reflection.
func extractIDFromValue(value ent.Value) (uuid.UUID, error) {
	if value == nil {
		return uuid.Nil, fmt.Errorf("%w: nil value", ErrExtractEntityID)
	}

	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return uuid.Nil, fmt.Errorf("%w: expected struct, got %T", ErrExtractEntityID, value)
	}

	idField := rv.FieldByName(fieldnames.FieldID.String())
	if !idField.IsValid() {
		return uuid.Nil, fmt.Errorf("%w: no ID field in struct", ErrExtractEntityID)
	}

	id, ok := idField.Interface().(uuid.UUID)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: ID field is %T, expected uuid.UUID", ErrExtractEntityID, idField.Interface())
	}

	return id, nil
}

// isSoftDelete checks if a mutation is a soft delete (setting deleted_at to a non-zero time).
func isSoftDelete(m ent.Mutation) bool {
	deletedAt, exists := m.Field(fieldnames.DBColumnDeletedAt)
	if !exists {
		return false
	}

	if t, ok := deletedAt.(time.Time); ok && !t.IsZero() {
		return true
	}

	return false
}

// buildOutboxEntry creates an outbox entry from the mutation context.
func buildOutboxEntry(ctx context.Context, config HookConfig, schema string, op string, entityID uuid.UUID, data any, beforeData any) (*OutboxEntry, error) {
	req := request.ForContext(ctx)
	user := req.User()

	// Transaction-scoped UUID generated by gqltx at BeginTx. Persisted in the
	// outbox row and used as the NATS message ID component for deterministic,
	// tx-scoped dedup. Distinct per OCC attempt by construction.
	transactionID, err := TransactionIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Trace ID and request ID are observability-only — they may be empty
	// when the trace context is missing or the request did not carry a
	// pyck.request-id baggage member (system jobs, internal services).
	traceID := TraceIDFromContext(ctx)
	requestID := RequestIDFromContext(ctx)

	// Get tenant ID from entity or context
	tenantID := extractTenantID(data)
	if tenantID == uuid.Nil {
		if req.HasMutationTenantID() {
			tenantID = req.MutationTenantID()
		} else {
			return nil, nil //nolint:nilnil // nil entry signals caller to skip outbox insertion
		}
	}

	// Start with context-provided search attributes
	ctxAttrs := ExtraSearchAttributesFromContext(ctx)
	searchAttrs := make(map[string]string, len(ctxAttrs)+5)
	maps.Copy(searchAttrs, ctxAttrs)

	// Merge default search attributes from entity (these take precedence over context attrs)
	maps.Copy(searchAttrs, BuildSearchAttributes(config.Service, data, entityID, tenantID))

	if _, hasDataType := searchAttrs[searchattributes.PyckDataTypeKey]; !hasDataType {
		if dt := inferDataType(schema); dt != "" {
			searchAttrs[searchattributes.PyckDataTypeKey] = dt
		}
	}

	// Build the payload
	payload := &MutationEventMessage{
		Service:            config.Service,
		Type:               strings.ToLower(config.Service) + schema,
		Schema:             schema,
		Operation:          op,
		ID:                 entityID,
		TenantID:           tenantID,
		DataBefore:         beforeData,
		DataAfter:          data,
		WfSearchAttributes: searchAttrs,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Build topic
	expectsReply := ExpectsReply(ctx)
	var topic Topic
	if expectsReply {
		topic = &MutationEventWithReplyTopic{
			StreamName:    config.StreamName,
			TenantID:      tenantID,
			ServiceName:   config.Service,
			SchemaName:    schema,
			EntityID:      entityID,
			OperationName: op,
		}
	} else {
		topic = &MutationEventTopic{
			StreamName:    config.StreamName,
			TenantID:      tenantID,
			ServiceName:   config.Service,
			SchemaName:    schema,
			EntityID:      entityID,
			OperationName: op,
		}
	}

	// Build user ID pointer
	var userIDPtr *uuid.UUID
	if user.ID != uuid.Nil {
		userIDPtr = &user.ID
	}

	return &OutboxEntry{
		ID:            uuidgql.GenerateV7UUID(),
		CreatedAt:     time.Now().UTC(),
		TransactionID: transactionID,
		TraceID:       traceID,
		RequestID:     requestID,
		UserID:        userIDPtr,
		Topic:         topic,
		Payload:       payloadBytes,
		WithReply:     expectsReply,
		EntityType:    &schema,
		EntityID:      &entityID,
		TenantID:      tenantID,
	}, nil
}

// extractTenantID extracts tenant ID from an entity using interface or reflection.
func extractTenantID(entity any) uuid.UUID {
	if entity == nil {
		return uuid.Nil
	}

	// Try interface first
	if te, ok := entity.(TenantIDer); ok {
		return te.GetTenantID()
	}

	// Fall back to reflection
	rv := reflect.ValueOf(entity)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return uuid.Nil
	}

	tenantField := rv.FieldByName(fieldnames.FieldTenantID.String())
	if !tenantField.IsValid() {
		return uuid.Nil
	}

	if id, ok := tenantField.Interface().(uuid.UUID); ok {
		return id
	}

	return uuid.Nil
}

// insertOutboxEntry inserts an entry into the outbox table using the configured OutboxInserter.
func insertOutboxEntry(ctx context.Context, config HookConfig, entry *OutboxEntry) error {
	if config.OutboxInserter == nil {
		return fmt.Errorf("%w: OutboxInserter not configured", ErrInsertOutbox)
	}
	return config.OutboxInserter(ctx, entry)
}
