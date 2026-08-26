// Package dataindex resolves the datatype's x-indices block: which path inside a
// data payload feeds which indexed slot column.
//
// It is the single surface shared by the projection hook (which writes slots) and
// the query resolver (which reads them), so a name resolves to the same slot on
// both sides by construction.
package dataindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// The errors a binding can be rejected with. Callers match on these rather than
// on message text: the validation hook turns them into the reason a datatype
// save was refused, and the query path into the reason a filter was.
var (
	ErrUnknownSlot      = errors.New("unknown slot")
	ErrSlotCollision    = errors.New("slot bound more than once")
	ErrBindingFrozen    = errors.New("index binding is frozen")
	ErrNotAssignable    = errors.New("source type not assignable to slot")
	ErrUnknownIndex     = errors.New("no index with that name")
	ErrBadSourcePath    = errors.New("source pointer is not a usable path")
	ErrUnsafeIdentifier = errors.New("not a safe SQL identifier")
)

// quoteIdent validates a name is a safe identifier and returns it quoted for
// Postgres. Values reaching the backfill originate in another service's datatype
// row, so this path re-checks rather than trusting them: fmt's %q is Go quoting,
// not Postgres identifier quoting, and would mangle an unexpected name instead of
// rejecting it.
func quoteIdent(name string) (string, error) {
	if !db.IsSafeIdentifier(name) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeIdentifier, name)
	}
	return `"` + name + `"`, nil
}

// Binding maps a caller-facing index name to a payload path and the slot it
// feeds. The name is the stable API handle; source is decoupled from it so a
// field can be nested or renamed without breaking queries.
type Binding struct {
	Name   string `json:"-"`
	Source string `json:"source"`
	Slot   string `json:"slot"`
}

// Kind returns the slot's type, encoded in its column name.
func (b Binding) Kind() string {
	rest, ok := strings.CutPrefix(b.Slot, mixin.SlotPrefix)
	if !ok {
		return ""
	}
	return strings.TrimRight(rest, "0123456789")
}

// Bindings is the parsed x-indices block, keyed by index name.
type Bindings map[string]Binding

// Parse reads the x-indices block out of a datatype's JSONSchema. A schema
// without the block simply has no indexed keys.
func Parse(jsonSchema string) (Bindings, error) {
	if strings.TrimSpace(jsonSchema) == "" {
		return Bindings{}, nil
	}
	var doc struct {
		XIndices map[string]Binding `json:"x-indices"`
	}
	if err := json.Unmarshal([]byte(jsonSchema), &doc); err != nil {
		return nil, fmt.Errorf("parse datatype schema: %w", err)
	}

	out := make(Bindings, len(doc.XIndices))
	for name, ix := range doc.XIndices {
		ix.Name = name
		out[name] = ix
	}
	return out, nil
}

// SlotPool is the set of slot columns an entity actually has, so a datatype
// cannot bind a column that was never allocated.
type SlotPool map[string]struct{}

// NewSlotPool builds the pool an entity's DataIndexMixin declares.
func NewSlotPool(m mixin.DataIndexMixin) SlotPool {
	pool := SlotPool{}
	for _, k := range []struct {
		kind string
		n    int
	}{
		{mixin.SlotKindText, m.Text},
		{mixin.SlotKindNumeric, m.Numeric},
		{mixin.SlotKindBool, m.Bool},
		{mixin.SlotKindList, m.List},
	} {
		for i := 1; i <= k.n; i++ {
			pool[mixin.SlotName(k.kind, i)] = struct{}{}
		}
	}
	return pool
}

// Has reports whether the entity actually carries this slot column. Bindings
// arrive from another service's datatype row, so every path that turns one into
// SQL checks it against the pool first.
func (p SlotPool) Has(slot string) bool {
	_, ok := p[slot]
	return ok
}

// Project extracts source out of the payload and coerces it to the slot's kind.
// A missing path yields nil, which clears the slot -- the row simply stops
// matching that index.
func Project(data map[string]any, b Binding) (any, error) {
	raw, ok := resolvePointer(data, b.Source)
	if !ok || raw == nil {
		// Absent is not an error: the row simply has nothing to index here.
		return nil, nil //nolint:nilnil
	}

	switch b.Kind() {
	case mixin.SlotKindText:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %s holds %T, want string", ErrNotAssignable, b.Source, raw)
		}
		return s, nil

	case mixin.SlotKindNumeric:
		// A JSON number only (data is JSON-decoded, so every number is a float64):
		// a numeric string cannot be reproduced by the backfill's jsonb_typeof
		// guard, and accepting it here would index a row the backfill would not.
		v, ok := raw.(float64)
		if !ok {
			return nil, fmt.Errorf("%w: %s holds %T, want number", ErrNotAssignable, b.Source, raw)
		}
		return v, nil

	case mixin.SlotKindBool:
		v, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: %s holds %T, want bool", ErrNotAssignable, b.Source, raw)
		}
		return v, nil

	case mixin.SlotKindList:
		arr, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s holds %T, want array", ErrNotAssignable, b.Source, raw)
		}
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			s, ok := renderListElement(e)
			if !ok {
				return nil, fmt.Errorf("%w: %s holds a non-scalar element (%T)", ErrNotAssignable, b.Source, e)
			}
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownSlot, b.Slot)
	}
}

// renderListElement renders one list element to the text the slot stores, and
// reports whether it is a scalar the index supports. It defines the rendering
// contract that backfillListExpr mirrors in SQL, so a row matches the same
// queries however it was indexed. Non-scalar elements (objects, arrays) have no
// shared rendering and are rejected.
//
// Numbers use Go's shortest round-trip in plain (never-exponent) notation. The
// SQL side matches by rendering float8 to its shortest text, then through numeric
// to strip any exponent: backfillListExpr. FormatFloat handles integers too (no
// trailing ".0"), so there is no int64 fast path to overflow at 2^63.
func renderListElement(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	case nil:
		return "", true
	default:
		return "", false
	}
}

// resolvePointer walks a JSON pointer (RFC 6901) into the payload.
//
// It splits with pointerSegments, the same rule the SQL path literal is built
// from, so the hook and the backfill cannot disagree about which key a source
// names -- the invariant the whole slot design rests on. A pointer that rule
// rejects resolves to nothing rather than to a different key than SQL reads.
func resolvePointer(data map[string]any, pointer string) (any, bool) {
	if pointer == "" || pointer == "/" {
		return data, true
	}
	segments, err := pointerSegments(pointer)
	if err != nil {
		return nil, false
	}
	var cur any = data
	for _, token := range segments {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[token]; !ok {
			return nil, false
		}
	}
	return cur, true
}
