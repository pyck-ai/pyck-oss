// Package jsonpatch provides RFC 6902 JSON Patch support for JSONB data fields.
//
// It wraps the evanphx/json-patch library and integrates with the pyck
// validator for DataType schema validation and uniqueness checks.
package jsonpatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	jsonpatchlib "gopkg.in/evanphx/json-patch.v4"

	"github.com/pyck-ai/pyck/backend/common/validator"
)

var (
	ErrEmptyPatches = errors.New("patches must not be empty")
	ErrInvalidOp    = errors.New("invalid patch operation")
	ErrMissingValue = errors.New("value is required for add, replace, and test operations")
	ErrMissingFrom  = errors.New("from is required for move and copy operations")
	ErrPatchFailed  = errors.New("failed to apply patch")
	ErrTestFailed   = jsonpatchlib.ErrTestFailed
)

type (
	// PatchOperation represents a single RFC 6902 JSON Patch operation.
	//
	// Op is the operation type (ADD, REMOVE, REPLACE, MOVE, COPY, TEST).
	// Path is the target location as a JSON Pointer (RFC 6901).
	// Value is the JSON-encoded value (required for ADD, REPLACE, TEST).
	// From is the source JSON Pointer (required for MOVE, COPY).
	PatchOperation struct {
		Op    JSONPatchOp
		Path  string
		Value *string
		From  *string
	}

	// PatchParams contains the parameters for [PatchEntityData].
	//
	// CurrentData is the entity's existing data map (nil is treated as empty).
	// DataTypeID and DataTypeSlug identify the JSON Schema for validation;
	// zero values are treated as unset. Patches are the RFC 6902 operations
	// to apply. Validator, Executor, TableName, FieldName, and DbDriver are
	// forwarded to the uniqueness check. EntityID is excluded from the
	// uniqueness query.
	PatchParams struct {
		CurrentData  map[string]any
		DataTypeID   *uuid.UUID
		DataTypeSlug *string
		Patches      []PatchOperation
		Validator    *validator.Validator
		Executor     validator.QueryExecutor
		TableName    string
		FieldName    string
		DbDriver     string
		EntityID     uuid.UUID
	}
)

// PatchEntityData applies RFC 6902 JSON Patch operations to the entity's data
// field, validates the result against the DataType schema, and checks
// uniqueness constraints.
//
// If CurrentData is nil (entity created without data), it is treated as an
// empty object. Zero-value DataTypeID/DataTypeSlug are treated as unset.
//
// Returns the patched data ready to be written back to the database.
func PatchEntityData(ctx context.Context, params PatchParams) (map[string]any, error) {
	if err := ValidateOperations(params.Patches); err != nil {
		return nil, fmt.Errorf("validate patch operations: %w", err)
	}

	// Treat nil data as empty object so patches can add fields to new entities.
	currentData := params.CurrentData
	if currentData == nil {
		currentData = make(map[string]any)
	}

	patched, err := ApplyPatches(currentData, params.Patches)
	if err != nil {
		return nil, err
	}

	// Convert zero values to nil so the validator treats them as "not set"
	// rather than trying to look up a zero UUID.
	dataTypeID := params.DataTypeID
	if dataTypeID != nil && *dataTypeID == uuid.Nil {
		dataTypeID = nil
	}
	dataTypeSlug := params.DataTypeSlug
	if dataTypeSlug != nil && *dataTypeSlug == "" {
		dataTypeSlug = nil
	}

	// Validate against DataType schema (reuses existing validator).
	// Use non-strict mode so entities without a DataType can still be patched.
	strict := dataTypeID != nil || dataTypeSlug != nil
	dataType, err := params.Validator.ValidateDataTypeInput(
		ctx, strict, patched, dataTypeID, dataTypeSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("validate patched data: %w", err)
	}

	// Check uniqueness constraints (only when a DataType with schema is available).
	if dataType == nil {
		return patched, nil
	}
	if err = params.Validator.ValidateInputDataUniqueness(ctx, params.Executor, validator.UniquenessValidationParams{
		Input:     patched,
		DataType:  dataType,
		TableName: params.TableName,
		FieldName: params.FieldName,
		DbDriver:  params.DbDriver,
		ExcludeID: &params.EntityID,
	}); err != nil {
		return nil, fmt.Errorf("validate patched data uniqueness: %w", err)
	}

	return patched, nil
}

// ApplyPatches converts operations to RFC 6902 JSON and applies them to the
// document. Returns the patched document as a new map. The original is not
// modified. Returns [ErrPatchFailed] if the patch cannot be applied (e.g.
// replacing a non-existent path, or a failing test operation).
func ApplyPatches(current map[string]any, ops []PatchOperation) (map[string]any, error) {
	docBytes, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("marshal current data: %w", err)
	}

	patchJSON, err := marshalPatchOps(ops)
	if err != nil {
		return nil, fmt.Errorf("marshal patch operations: %w", err)
	}

	patch, err := jsonpatchlib.DecodePatch(patchJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPatchFailed, err)
	}

	resultBytes, err := patch.Apply(docBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPatchFailed, err)
	}

	var result map[string]any
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return nil, fmt.Errorf("unmarshal patched data: %w", err)
	}

	return result, nil
}

// ValidateOperations checks that all operations are well-formed.
func ValidateOperations(ops []PatchOperation) error {
	if len(ops) == 0 {
		return ErrEmptyPatches
	}

	for i, op := range ops {
		switch op.Op {
		case JSONPatchOpAdd, JSONPatchOpReplace, JSONPatchOpTest:
			if op.Value == nil {
				return fmt.Errorf("patch[%d]: %w (op=%q)", i, ErrMissingValue, op.Op)
			}
		case JSONPatchOpRemove:
			// value and from are optional
		case JSONPatchOpMove, JSONPatchOpCopy:
			if op.From == nil {
				return fmt.Errorf("patch[%d]: %w (op=%q)", i, ErrMissingFrom, op.Op)
			}
		default:
			return fmt.Errorf("patch[%d]: %w: %q", i, ErrInvalidOp, op.Op)
		}
	}

	return nil
}

// marshalPatchOps converts PatchOperation slice to RFC 6902 JSON array.
func marshalPatchOps(ops []PatchOperation) ([]byte, error) {
	type rfc6902Op struct {
		Op    string           `json:"op"`
		Path  string           `json:"path"`
		Value *json.RawMessage `json:"value,omitempty"`
		From  string           `json:"from,omitempty"`
	}

	rfc := make([]rfc6902Op, len(ops))
	for i, op := range ops {
		r := rfc6902Op{
			Op:   strings.ToLower(op.Op.String()),
			Path: op.Path,
		}
		if op.Value != nil {
			raw := json.RawMessage(*op.Value)
			r.Value = &raw
		}
		if op.From != nil {
			r.From = *op.From
		}
		rfc[i] = r
	}

	return json.Marshal(rfc)
}
