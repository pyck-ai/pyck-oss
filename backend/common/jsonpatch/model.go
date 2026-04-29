//go:generate -command enumer go tool enumer -text -json -yaml -sql -gqlgen -typederrors

package jsonpatch

// JSONPatchOp represents an RFC 6902 JSON Patch operation type.
//
//go:generate enumer -output=jsonpatchop_gen.go -type=JSONPatchOp -linecomment
type JSONPatchOp uint

const (
	JSONPatchOpAdd     JSONPatchOp = iota + 1 // ADD
	JSONPatchOpRemove                         // REMOVE
	JSONPatchOpReplace                        // REPLACE
	JSONPatchOpMove                           // MOVE
	JSONPatchOpCopy                           // COPY
	JSONPatchOpTest                           // TEST
)

// JSONPatchInput is the GraphQL input type for a single RFC 6902 operation.
type JSONPatchInput struct {
	Op    JSONPatchOp `json:"op"`
	Path  string      `json:"path"`
	Value *string     `json:"value,omitempty"`
	From  *string     `json:"from,omitempty"`
}

// ToOperation converts a GraphQL input to an internal PatchOperation.
func (j JSONPatchInput) ToOperation() PatchOperation {
	return PatchOperation(j)
}

// ToOperations converts a slice of GraphQL inputs to internal PatchOperations.
// Nil elements are skipped.
func ToOperations(inputs []*JSONPatchInput) []PatchOperation {
	ops := make([]PatchOperation, 0, len(inputs))
	for _, input := range inputs {
		if input == nil {
			continue
		}
		ops = append(ops, input.ToOperation())
	}
	return ops
}
