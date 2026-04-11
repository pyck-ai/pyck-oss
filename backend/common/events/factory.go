package events

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/pyck-ai/pyck/backend/common/internal/fieldnames"
	"github.com/pyck-ai/pyck/backend/common/internal/searchattributes"
	"github.com/pyck-ai/pyck/backend/common/request"
)

var (
	ErrEntityNotStruct = errors.New("entity must be struct or *struct")
	ErrMissingIDField  = errors.New("entity missing ID field")
	ErrIDNotUUID       = errors.New("ID is not uuid.UUID")
)

// SearchAttributes is a named type for workflow search attributes.
type SearchAttributes map[string]string

// Option is a named type for message customizers.
type Option func(*MutationEventMessage)

// WorkflowSpec holds optional workflow extras (merged into search attributes).
type WorkflowSpec struct {
	Extra map[string]string
}

// Factory builds MutationEventMessage consistently across services.
type Factory struct {
	service string
}

// NewFactory creates a new Factory bound to a service name (one per service is typical).
func NewFactory(service string) *Factory {
	return &Factory{service: service}
}

// schemaCache caches type names to avoid repeated reflection.
var schemaCache sync.Map

// getSchemaName returns the type name of the entity (cached).
func getSchemaName(entity any) string {
	t := reflect.TypeOf(entity)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if cached, ok := schemaCache.Load(t); ok {
		if name, ok := cached.(string); ok {
			return name
		}
	}

	name := t.Name()
	schemaCache.Store(t, name)
	return name
}

// Build constructs a MutationEventMessage for the given operation and entity.
func (f *Factory) Build(req request.RequestContext, op string, entity any, wf *WorkflowSpec, opts ...Option) (*MutationEventMessage, error) {
	// Prefer interfaces over reflection for extracting ID and TenantID.
	var id uuid.UUID
	var tenantID uuid.UUID

	// Try IDer interface first, fall back to reflection.
	if ider, ok := entity.(IDer); ok {
		id = ider.GetID()
	} else {
		extractedID, err := extractIDViaReflection(entity)
		if err != nil {
			return nil, err
		}
		id = extractedID
	}

	// Try TenantIDer interface first, fall back to reflection.
	if tider, ok := entity.(TenantIDer); ok {
		tenantID = tider.GetTenantID()
	} else {
		tenantID = extractTenantIDViaReflection(entity)
	}
	if tenantID == uuid.Nil {
		if req.HasMutationTenantID() {
			tenantID = req.MutationTenantID()
		}
	}

	// Get schema name from type (cached).
	schema := getSchemaName(entity)
	titleService := cases.Title(language.English).String(f.service)

	msg := &MutationEventMessage{
		Service:   f.service,
		Operation: op,
		Type:      titleService + schema,
		Schema:    schema,
		ID:        id,
		TenantID:  tenantID,
		DataAfter: entity,
	}

	// Core WF attributes available to all consumers.
	msg.WfSearchAttributes = SearchAttributes{
		searchattributes.PyckTenantIDKey: tenantID.String(),
		searchattributes.PyckServiceKey:  f.service,
	}

	// Default data-type/data-id derived from schema and entity ID.
	if dt := inferDataType(schema); dt != "" {
		msg.WfSearchAttributes[searchattributes.PyckDataTypeKey] = dt
		msg.WfSearchAttributes[searchattributes.PyckDataIDKey] = id.String()
	}

	// Merge optional extras.
	if wf != nil && wf.Extra != nil {
		for k, v := range wf.Extra {
			msg.WfSearchAttributes[k] = v
		}
	}

	// Apply user-provided overrides.
	for _, opt := range opts {
		opt(msg)
	}

	return msg, nil
}

// extractIDViaReflection extracts the ID field using reflection.
// This is a fallback when the entity doesn't implement IDer.
func extractIDViaReflection(entity any) (uuid.UUID, error) {
	rv := reflect.ValueOf(entity)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return uuid.Nil, fmt.Errorf("%w: got %T", ErrEntityNotStruct, entity)
	}

	idField := rv.FieldByName(fieldnames.FieldID.String())
	if !idField.IsValid() {
		return uuid.Nil, ErrMissingIDField
	}

	id, ok := idField.Interface().(uuid.UUID)
	if !ok {
		return uuid.Nil, ErrIDNotUUID
	}
	return id, nil
}

// extractTenantIDViaReflection extracts the TenantID field using reflection.
// This is a fallback when the entity doesn't implement TenantIDer.
// Returns uuid.Nil if the field doesn't exist.
func extractTenantIDViaReflection(entity any) uuid.UUID {
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

	if tid, ok := tenantField.Interface().(uuid.UUID); ok {
		return tid
	}
	return uuid.Nil
}

// WithType overrides the auto-generated Type field.
func WithType(t string) Option {
	return func(m *MutationEventMessage) {
		m.Type = t
	}
}

// WithSchema overrides the auto-generated Schema field.
func WithSchema(s string) Option {
	return func(m *MutationEventMessage) {
		m.Schema = s
	}
}

// WithTenantID overrides TenantID and synchronizes the tenant search attribute.
func WithTenantID(tid uuid.UUID) Option {
	return func(m *MutationEventMessage) {
		m.TenantID = tid
		if m.WfSearchAttributes == nil {
			m.WfSearchAttributes = make(SearchAttributes, 1)
		}

		m.WfSearchAttributes[searchattributes.PyckTenantIDKey] = tid.String()
	}
}

// WithSearchAttributes merges custom workflow search attributes (overwrites on key collision).
func WithSearchAttributes(attrs map[string]string) Option {
	return func(m *MutationEventMessage) {
		if m.WfSearchAttributes == nil {
			m.WfSearchAttributes = make(SearchAttributes, len(attrs))
		}

		for k, v := range attrs {
			m.WfSearchAttributes[k] = v
		}
	}
}

// WithSearchAttributesDataType sets the data type and aligns data ID to the event ID.
func WithSearchAttributesDataType(dt string) Option {
	return func(m *MutationEventMessage) {
		if m.WfSearchAttributes == nil {
			m.WfSearchAttributes = make(SearchAttributes, 2)
		}

		m.WfSearchAttributes[searchattributes.PyckDataTypeKey] = dt
		m.WfSearchAttributes[searchattributes.PyckDataIDKey] = m.ID.String()
	}
}

// WithSearchAttributesDataID sets the data ID search attribute.
func WithSearchAttributesDataID(id string) Option {
	return func(m *MutationEventMessage) {
		if m.WfSearchAttributes == nil {
			m.WfSearchAttributes = make(SearchAttributes, 1)
		}
		m.WfSearchAttributes[searchattributes.PyckDataIDKey] = id
	}
}

// WithSearchAttributesAssignee sets/overrides the workflow assignee.
func WithSearchAttributesAssignee(assignee string) Option {
	return func(m *MutationEventMessage) {
		if m.WfSearchAttributes == nil {
			m.WfSearchAttributes = make(map[string]string)
		}
		m.WfSearchAttributes[searchattributes.PyckWorkflowAssigneeKey] = assignee
	}
}

// inferDataType computes a default "pyck_data_type" from schema, e.g. "Item" -> "itemID".
func inferDataType(schema string) string {
	if schema == "" {
		return ""
	}

	runes := []rune(schema)
	runes[0] = toLowerRune(runes[0])

	return string(runes) + "ID"
}

// toLowerRune lowercases a single ASCII letter rune without allocations.
func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}

	return r
}
