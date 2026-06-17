package idempotency

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
)

// OperationChecksum returns a deterministic 32-byte sha256 hash that
// identifies a GraphQL mutation request for idempotency purposes. Re-using
// an idempotency key with any materially different request must be rejected
// as a 422, so the checksum covers all three inputs that shape the cached
// response:
//
//   - the operation name,
//   - the full operation text including the selection set — two requests
//     with the same name and variables but a different selection set must
//     NOT replay each other's body, which would be shaped for the wrong
//     fields, and
//   - the variables, with schema-declared defaults applied so a retry that
//     omits a defaulted variable matches a call that supplies the same
//     value explicitly.
//
// The operation is canonicalized by re-printing its AST in compact form, so
// insignificant whitespace and layout differences do not change the
// checksum. Variables are canonicalized with object keys sorted at every
// nesting level. A non-marshalable variable or an un-coercible default
// surfaces as an error so the caller can reject the request instead of
// mapping every encoding failure to the same checksum (which would let a
// bad-payload retry false-match a previous bad payload's committed row).
func OperationChecksum(op *ast.OperationDefinition, fragments ast.FragmentDefinitionList, vars map[string]any) ([32]byte, error) {
	if op == nil {
		return [32]byte{}, ErrNilOperation
	}

	h := sha256.New()
	h.Write([]byte(op.Name))
	h.Write([]byte{0x1f}) // unit separator: prevent name/body boundary collisions

	h.Write([]byte(canonicalOperation(op, fragments)))
	h.Write([]byte{0x1f}) // separator between operation text and variables

	merged, err := applyVariableDefaults(op, vars)
	if err != nil {
		return [32]byte{}, fmt.Errorf("apply variable defaults: %w", err)
	}
	enc, err := canonicalJSON(merged)
	if err != nil {
		return [32]byte{}, fmt.Errorf("canonical-json variables: %w", err)
	}
	h.Write(enc)

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// canonicalOperation re-prints the operation (plus any fragment definitions
// it may reference) in gqlparser's compact form, giving a stable encoding of
// the operation text — including its selection set — that is insensitive to
// insignificant whitespace and layout. Only the selected operation is
// included, so unrelated operations in a multi-operation document do not
// affect the checksum.
func canonicalOperation(op *ast.OperationDefinition, fragments ast.FragmentDefinitionList) string {
	var sb strings.Builder
	formatter.NewFormatter(&sb, formatter.WithCompacted()).
		FormatQueryDocument(&ast.QueryDocument{
			Operations: ast.OperationList{op},
			Fragments:  fragments,
		})
	return sb.String()
}

// applyVariableDefaults returns vars with any schema-declared default filled
// in for variables the request omitted. This makes the checksum semantic
// rather than structural for variables: a call that relies on a default and
// a later retry that supplies the same value explicitly produce identical
// checksums. A variable supplied as an explicit null is left as null (a
// present null is not "absent"). The input map is never mutated.
func applyVariableDefaults(op *ast.OperationDefinition, vars map[string]any) (map[string]any, error) {
	hasDefault := false
	for _, def := range op.VariableDefinitions {
		if def.DefaultValue != nil {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		return vars, nil
	}

	merged := make(map[string]any, len(vars)+len(op.VariableDefinitions))
	for k, v := range vars {
		merged[k] = v
	}
	for _, def := range op.VariableDefinitions {
		if def.DefaultValue == nil {
			continue
		}
		if _, ok := merged[def.Variable]; ok {
			continue
		}
		dv, err := def.DefaultValue.Value(nil)
		if err != nil {
			return nil, fmt.Errorf("default for $%s: %w", def.Variable, err)
		}
		merged[def.Variable] = dv
	}
	return merged, nil
}

// canonicalJSON encodes v as JSON with deterministic object-key ordering at
// every nesting level. Non-map values are encoded with the stdlib encoder.
func canonicalJSON(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		return canonicalJSONObject(t)
	case []any:
		return canonicalJSONArray(t)
	default:
		return json.Marshal(v)
	}
}

func canonicalJSONObject(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		out = append(out, keyJSON...)
		out = append(out, ':')
		valJSON, err := canonicalJSON(m[k])
		if err != nil {
			return nil, err
		}
		out = append(out, valJSON...)
	}
	out = append(out, '}')
	return out, nil
}

func canonicalJSONArray(a []any) ([]byte, error) {
	if a == nil {
		return []byte("null"), nil
	}
	out := []byte{'['}
	for i, item := range a {
		if i > 0 {
			out = append(out, ',')
		}
		itemJSON, err := canonicalJSON(item)
		if err != nil {
			return nil, err
		}
		out = append(out, itemJSON...)
	}
	out = append(out, ']')
	return out, nil
}
