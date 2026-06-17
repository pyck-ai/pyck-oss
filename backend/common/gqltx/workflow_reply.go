package gqltx

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	// workflowsFieldName is the GraphQL field name for workflow details on mutation return types.
	workflowsFieldName = "workflows"
)

// WorkflowReplyMiddleware automatically handles workflow reply registration
// for mutation resolvers whose return types have a Workflows field.
//
// This middleware:
//  1. Detects if a mutation resolver returns a type with a settable Workflows field
//  2. Automatically sets up reply registration before the resolver executes
//  3. After commit, waits for the reply and sets the Workflows field via reflection
//
// This eliminates boilerplate from resolvers - they only need to contain business logic.
type WorkflowReplyMiddleware struct {
	replyRegistry  *events.ReplyRegistry
	replyTimeout   time.Duration
	workflowFields map[string]bool
}

// NewWorkflowReplyMiddleware creates a new workflow reply middleware.
func NewWorkflowReplyMiddleware(replyRegistry *events.ReplyRegistry, replyTimeout time.Duration) *WorkflowReplyMiddleware {
	return &WorkflowReplyMiddleware{
		replyRegistry: replyRegistry,
		replyTimeout:  replyTimeout,
	}
}

// ExtensionName implements graphql.HandlerExtension.
func (m *WorkflowReplyMiddleware) ExtensionName() string {
	return "WorkflowReplyMiddleware"
}

// Validate implements graphql.HandlerExtension.
// It walks the schema's Mutation type and caches which mutations return a type
// with a "workflows" field. Only those mutations will use request/reply.
func (m *WorkflowReplyMiddleware) Validate(es graphql.ExecutableSchema) error {
	m.workflowFields = make(map[string]bool)

	schema := es.Schema()
	mutationType, ok := schema.Types["Mutation"]
	if !ok {
		return nil
	}

	for _, field := range mutationType.Fields {
		returnTypeName := field.Type.Name()
		if returnTypeName == "" {
			continue
		}
		returnType, ok := schema.Types[returnTypeName]
		if !ok {
			continue
		}
		if returnType.Fields.ForName(workflowsFieldName) != nil {
			m.workflowFields[field.Name] = true
		}
	}

	return nil
}

// InterceptField wraps mutation field resolvers to automatically handle workflow replies.
func (m *WorkflowReplyMiddleware) InterceptField(ctx context.Context, next graphql.Resolver) (interface{}, error) {
	logger := log.ForContext(ctx)

	// Only intercept mutations
	fc := graphql.GetFieldContext(ctx)
	if fc == nil {
		return next(ctx)
	}

	oc := graphql.GetOperationContext(ctx)
	if oc == nil || oc.Operation == nil || oc.Operation.Operation != ast.Mutation {
		return next(ctx)
	}

	// Only intercept top-level mutation fields (not nested fields)
	// Top-level fields have a path with exactly one segment
	if len(fc.Path()) != 1 {
		return next(ctx)
	}

	logger.Debug().Str("field", fc.Field.Name).Msg("intercepting mutation field")

	// Set up reply registration before the resolver executes.
	// Use the per-tx UUID (regenerated on every OCC retry) so the rolled-back
	// attempt's waiter cannot accidentally absorb the successful attempt's reply.
	transactionID, err := events.TransactionIDFromContext(ctx)
	if err != nil {
		// No transaction ID — gqltx middleware did not run on this ctx;
		// proceed without reply handling.
		logger.Debug().Err(err).Msg("no transaction ID on ctx, skipping workflow reply")
		return next(ctx)
	}

	logger.Debug().Str("transaction_id", transactionID.String()).Msg("registering workflow reply")

	// When async signals are enabled, skip reply registration entirely.
	// The outbox handler will publish fire-and-forget instead of request/reply,
	// so there's nothing to wait for.
	if feature.HasFeature(ctx, feature.FEATURE_ASYNC_SIGNALS) {
		logger.Debug().Msg("async signals enabled, skipping reply registration")
		return next(ctx)
	}

	// Only mutations whose return type has a "workflows" field need request/reply.
	// Others fall back to fire-and-forget via the outbox handler.
	if !m.workflowFields[fc.Field.Name] {
		logger.Debug().Str("field", fc.Field.Name).Msg("mutation return type has no workflows field, skipping reply")
		return next(ctx)
	}

	// Mark context for reply and register
	ctx = events.WithExpectReply(ctx, true)
	replyCh := m.replyRegistry.Register(transactionID, m.replyTimeout)

	// Execute the resolver
	result, err := next(ctx)
	if err != nil {
		logger.Debug().Err(err).Msg("resolver returned error")
		return result, err
	}

	// Check if result has a settable Workflows field via reflection
	setter := workflowsSetterFor(result)
	if setter == nil {
		logger.Debug().Str("result_type", fmt.Sprintf("%T", result)).Msg("no workflows setter found for result type")
		return result, nil
	}

	// Check if post-commit container exists (holds response patches)
	if !HasResponsePatches(ctx) {
		logger.Warn().Msg("post-commit container not found in context")
	} else {
		logger.Debug().Msg("adding workflow reply response patch")
	}

	fieldName := fc.Field.Name

	// Add response patch to wait for workflow IDs and patch the serialized JSON response.
	// This runs after tx.Commit() + post-commit hooks, so the JSON is already frozen
	// by the time the patch executes — we must patch r.Data directly.
	replyTimeout := m.replyTimeout
	AddResponsePatch(ctx, func(r *graphql.Response) error {
		logger.Debug().Str("transaction_id", transactionID.String()).Msg("waiting for workflow reply")

		var workflows []*events.WorkflowDetails
		select {
		case workflows = <-replyCh:
			logger.Debug().Str("transaction_id", transactionID.String()).Int("count", len(workflows)).Msg("received workflows from reply")
		case <-time.After(replyTimeout):
			logger.Debug().Str("transaction_id", transactionID.String()).Msg("timed out waiting for workflow reply")
			return nil
		case <-ctx.Done():
			logger.Debug().Str("transaction_id", transactionID.String()).Msg("context cancelled while waiting for workflow reply")
			return nil
		}

		if len(workflows) == 0 {
			return nil
		}

		// Also set on the struct for any edge resolvers that read the Go value.
		setter(workflows)

		// Patch the serialized JSON response.
		return PatchWorkflowsInResponse(r, fieldName, workflows)
	})

	return result, nil
}

// workflowsFieldInfo caches reflection metadata for types with a Workflows field.
type workflowsFieldInfo struct {
	fieldIndex int          // index of the Workflows field
	elemType   reflect.Type // struct type of slice elements (e.g., TemporalWorkflow)
}

// workflowsTypeCache caches whether a type has a compatible Workflows field.
// Values are *workflowsFieldInfo (compatible) or nil (not compatible).
var workflowsTypeCache sync.Map

// workflowsSetterFor returns a setter function if result has a Workflows []*T field
// where T has Type, ID, RunID string fields. Returns nil if not compatible.
func workflowsSetterFor(result any) func([]*events.WorkflowDetails) {
	rv := reflect.ValueOf(result)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return nil
	}

	rt := rv.Type()

	// Check cache first.
	cached, ok := workflowsTypeCache.Load(rt)
	if ok {
		info, _ := cached.(*workflowsFieldInfo)
		if info == nil {
			return nil
		}
		return makeWorkflowsSetter(rv, info)
	}

	// Discover: look for a Workflows field that is []*SomeStruct with Type/ID/RunID.
	info := discoverWorkflowsField(rt)
	workflowsTypeCache.Store(rt, info)
	if info == nil {
		return nil
	}
	return makeWorkflowsSetter(rv, info)
}

// discoverWorkflowsField checks if rt has a compatible Workflows field.
func discoverWorkflowsField(rt reflect.Type) *workflowsFieldInfo {
	f, ok := rt.FieldByName("Workflows")
	if !ok {
		return nil
	}
	// Must be a slice of pointers to structs.
	if f.Type.Kind() != reflect.Slice {
		return nil
	}
	elemPtr := f.Type.Elem()
	if elemPtr.Kind() != reflect.Ptr {
		return nil
	}
	elem := elemPtr.Elem()
	if elem.Kind() != reflect.Struct {
		return nil
	}
	// Must have Type, ID, RunID string fields.
	for _, name := range []string{"Type", "ID", "RunID"} {
		sf, exists := elem.FieldByName(name)
		if !exists || sf.Type.Kind() != reflect.String {
			return nil
		}
	}
	return &workflowsFieldInfo{
		fieldIndex: f.Index[0],
		elemType:   elem,
	}
}

// makeWorkflowsSetter builds a setter closure for the given struct value and field info.
func makeWorkflowsSetter(rv reflect.Value, info *workflowsFieldInfo) func([]*events.WorkflowDetails) {
	wf := rv.Field(info.fieldIndex)
	elemType := info.elemType
	sliceType := wf.Type()

	return func(workflows []*events.WorkflowDetails) {
		if len(workflows) == 0 {
			return
		}
		slice := reflect.MakeSlice(sliceType, len(workflows), len(workflows))
		for i, w := range workflows {
			elem := reflect.New(elemType)
			s := elem.Elem()
			s.FieldByName("Type").SetString(w.Type)
			s.FieldByName("ID").SetString(w.ID)
			s.FieldByName("RunID").SetString(w.RunID)
			slice.Index(i).Set(elem)
		}
		wf.Set(slice)
	}
}

// PatchWorkflowsInResponse patches the serialized JSON in r.Data to include workflow details.
// It navigates to the top-level field (fieldName) and sets its "workflows" key.
//
// Example: given fieldName="createInventoryItem" and r.Data=`{"createInventoryItem":{"id":"...","workflows":null}}`,
// it rewrites workflows to the JSON-encoded workflow details.
func PatchWorkflowsInResponse(r *graphql.Response, fieldName string, workflows []*events.WorkflowDetails) error {
	if len(r.Data) == 0 {
		return nil
	}

	// Decode the top-level JSON object.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &root); err != nil {
		return fmt.Errorf("gqltx: unmarshal response data: %w", err)
	}

	fieldRaw, ok := root[fieldName]
	if !ok || len(fieldRaw) == 0 || string(fieldRaw) == "null" {
		return nil
	}

	// Decode the mutation result object.
	var field map[string]json.RawMessage
	if err := json.Unmarshal(fieldRaw, &field); err != nil {
		return fmt.Errorf("gqltx: unmarshal field %q: %w", fieldName, err)
	}

	// Encode workflow details directly — events.WorkflowDetails already has
	// the correct JSON tags matching the GraphQL schema shape.
	wfBytes, err := json.Marshal(workflows)
	if err != nil {
		return fmt.Errorf("gqltx: marshal workflows: %w", err)
	}

	field[workflowsFieldName] = wfBytes

	// Re-encode and replace.
	fieldBytes, err := json.Marshal(field)
	if err != nil {
		return fmt.Errorf("gqltx: marshal field %q: %w", fieldName, err)
	}
	root[fieldName] = fieldBytes

	data, err := json.Marshal(root)
	if err != nil {
		return fmt.Errorf("gqltx: marshal response data: %w", err)
	}

	r.Data = data
	return nil
}
