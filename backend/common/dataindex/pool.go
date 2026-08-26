package dataindex

import (
	"encoding/json"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// Entity labels of the entities carrying a slot pool. The label is the datatype's
// `entity` field, which is how a datatype names what it describes.
const (
	EntityPickingOrder = "picking_order"
)

// pools is the one place slot pools are declared. The owning service's ent schema
// and the datatype validation both read it, so a datatype can never bind a slot
// the entity does not actually have -- they cannot drift into disagreeing.
var pools = map[string]mixin.DataIndexMixin{
	EntityPickingOrder: {Text: 4, Numeric: 2, Bool: 2, List: 2},
}

// PoolMixin returns the mixin an entity's ent schema embeds. An entity with no
// declared pool gets no slots.
func PoolMixin(entity string) mixin.DataIndexMixin {
	return pools[entity]
}

// PoolFor returns the slots an entity carries, and whether it carries any.
func PoolFor(entity string) (SlotPool, bool) {
	m, ok := pools[entity]
	if !ok {
		return nil, false
	}
	return NewSlotPool(m), true
}

// SchemaTypes reads the declared type of each top-level property, so a binding
// can be checked against the shape it claims to index.
//
// Only top-level properties are resolved; a binding into a nested path is left
// unchecked rather than wrongly rejected.
func SchemaTypes(jsonSchema string) (SchemaTypeAt, error) {
	if strings.TrimSpace(jsonSchema) == "" {
		return func(string) string { return "" }, nil
	}
	var doc struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(jsonSchema), &doc); err != nil {
		return nil, err
	}
	return func(pointer string) string {
		name, nested := strings.CutPrefix(pointer, "/")
		if !nested || strings.Contains(name, "/") {
			return ""
		}
		return doc.Properties[name].Type
	}, nil
}
