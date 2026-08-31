package validator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/pyck-ai/pyck/backend/common/db"
	common_jsonschema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
)

type Field struct {
	Type   string
	Name   string
	Unique bool
	Parent *Field
}

func (f Field) String() string {
	return strings.Join(f.Path(), ".")
}

func (f Field) Path() []string {
	var parts []string

	for curr := &f; curr.Parent != nil; curr = curr.Parent {
		parts = append(parts, curr.Name)
	}

	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}

	return parts
}

type UniquenessValidationParams struct {
	Input     map[string]any
	DataType  *common_jsonschema.DataType
	TableName string
	FieldName string
	DbDriver  string
	ExcludeID *uuid.UUID // Optional ID to exclude from uniqueness check
}

type QueryExecutor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Validator is responsible for validating JSON input against schema definitions
// and generating uniqueness queries when needed.
type Validator struct {
	dataTypeReader DataTypeReader
}

// NewValidator initializes the validator. Custom format validators (EAN, UPC,
// etc.) are registered on each schema compiler in validateInputWithSchema.
func NewValidator(dataTypeReader DataTypeReader) *Validator {
	return &Validator{
		dataTypeReader: dataTypeReader,
	}
}

func (v *Validator) ReadBySlug(ctx context.Context, slug string) (*common_jsonschema.DataType, error) {
	return v.dataTypeReader.ReadBySlug(ctx, slug)
}

func (v *Validator) ReadByID(ctx context.Context, id uuid.UUID) (*common_jsonschema.DataType, error) {
	return v.dataTypeReader.ReadByID(ctx, id)
}

// ReadFor resolves the datatype a row is written against, by ID first and slug
// only as a fallback, returning nil when neither identifies one. This is a write
// handle: the row is pinned to a specific data_type_id, whereas a slug resolves to
// the latest version -- so under datatype versioning the slug would project a row
// pinned to v2 using v5's bindings. (A query names the lineage and reads by slug;
// that is a different handle.) The cache reader's ReadByID also needs no
// authenticated user, which its ReadBySlug does.
func (v *Validator) ReadFor(ctx context.Context, dataTypeID uuid.UUID, dataTypeSlug string) (*common_jsonschema.DataType, error) {
	switch {
	case dataTypeID != uuid.Nil:
		return v.ReadByID(ctx, dataTypeID)
	case dataTypeSlug != "":
		return v.ReadBySlug(ctx, dataTypeSlug)
	default:
		return nil, nil //nolint:nilnil // neither identifier given: no datatype, no error
	}
}

// ValidateDataTypeInput resolves the DataType referenced by `dataTypeID` or
// `dataTypeSlug` and (when `input` is non-empty) validates the input map
// against the resolved version's JSON schema.
//
// Resolution priority (under #990's append-only DataType model):
//
//  1. dataTypeID set + non-Nil → ReadByID. If a dataTypeSlug is *also* supplied
//     it must match the resolved version's slug (ErrDataTypeSlugMismatch
//     otherwise) — the id wins, the slug is a consistency cross-check.
//  2. dataTypeID unset + dataTypeSlug set + non-empty → ReadBySlug → latest
//     non-deleted version for the slug. This is a first-class validator-API
//     capability, not a legacy path: entity-update resolvers happen to pin by
//     id, but the validator itself may legitimately resolve by slug (e.g. the
//     jsonpatch path, or any slug-addressed validation).
//  3. Neither set + data non-empty + strict=true → ErrDataTypeNotSet.
//  4. Neither set + data nil → no-op (returns nil, nil).
//
// When a DataType is resolved, it's returned to the caller even if `input`
// is nil — this lets resolvers read the resolved version's slug (to set the
// row's data_type_slug column) during an update-without-data flow ("change
// which schema version this row references, but don't touch the row's data").
// NOTE: this function validates only the supplied input; persisted-data
// revalidation on a repoint-without-data lives in the generated
// SetInputWithDataType update helper.
//
// Write-time guard: writes against a soft-deleted DataType row are rejected
// with ErrDataTypeDeleted even if the row is still resolvable by ID. Reads
// against deleted DataType refs remain allowed by the resolvers (they don't
// call this function on read).
//
//nolint:nilnil // (nil, nil) means "no DataType reference + no data — caller skips validation" (case 4 in the docstring); the generated SetInputWithDataType builder method wraps this for the resolver-side handling.
func (v *Validator) ValidateDataTypeInput(ctx context.Context, strict bool, input map[string]any, dataTypeID *uuid.UUID, dataTypeSlug *string) (*common_jsonschema.DataType, error) {
	var (
		dataType *common_jsonschema.DataType
		err      error
	)

	switch {
	case dataTypeID != nil && *dataTypeID != uuid.Nil:
		dataType, err = v.ReadByID(ctx, *dataTypeID)
	case dataTypeSlug != nil && *dataTypeSlug != "":
		dataType, err = v.ReadBySlug(ctx, *dataTypeSlug)
	default:
		// No DataType referenced. If data is set under strict mode, that's
		// an error (the entity wants a schema but the caller didn't name
		// one). Otherwise it's a no-op.
		if input != nil && strict {
			return nil, ErrDataTypeNotSet
		}
		return nil, nil
	}

	if err != nil {
		// Preserve non-NotFound errors so cancellation, network failures,
		// and similar transient errors aren't misdiagnosed as "data type
		// missing". The cache + DB providers all return ErrDataTypeNotFound
		// (or the entgen-wrapped equivalent) when the row truly doesn't
		// exist; everything else is a real failure worth surfacing.
		if errors.Is(err, ErrDataTypeNotFound) || errors.Is(err, common_jsonschema.ErrDataTypeNotFound) {
			return nil, ErrDataTypeNotFound
		}
		return nil, fmt.Errorf("resolve data type: %w", err)
	}
	if dataType == nil {
		return nil, nil
	}

	// Tenant isolation: a DataType owned by a tenant is served only to a caller
	// acting on exactly that tenant. In non-management services the cache holds
	// DataTypes from every tenant (it subscribes to all tenants' NATS events)
	// and its ReadByID is not tenant-scoped, so without this check a caller in
	// tenant A could pin to tenant B's data_type_id, get B's schema for
	// validation, and write a row under A pointing at a foreign DataType.
	// Management's DatabaseDataTypeProvider scopes both of its reads by the
	// mutation tenant, so there this is a second line of defence.
	//
	// Guards:
	//   - HasMutationTenantID: a context naming no single tenant cannot be
	//     matched against, so it is refused rather than served unscoped —
	//     matching DataTypesCache.ReadByID's fallback gate. Nothing legitimate
	//     is lost: every caller sits on a mutation path, and TenantMixin's
	//     mutation hook reads MutationTenantID unconditionally, so such a
	//     context panics before the write lands anyway.
	//   - TenantID != uuid.Nil: real DataTypes always carry a non-nil tenant
	//     (TenantMixin + NOT NULL column), so a zero tenant can only come from
	//     a tenant-agnostic test fixture.
	if dataType.TenantID != uuid.Nil {
		req := request.ForContext(ctx)
		if !req.HasMutationTenantID() || dataType.TenantID != req.MutationTenantID() {
			return nil, ErrDataTypeNotFound
		}
	}

	// Reject writes against deleted versions even though the row is still
	// resolvable by ID. Read paths don't reach this function.
	if dataType.DeletedAt != nil {
		return nil, ErrDataTypeDeleted
	}

	// When the caller pins by ID but also supplies a slug, the two must agree.
	// Resolution above used the ID (case 1); a divergent slug means the client's
	// (id → slug) view is stale or malformed, so fail loud rather than write a
	// row whose slug silently disagrees with its id.
	if dataTypeID != nil && *dataTypeID != uuid.Nil &&
		dataTypeSlug != nil && *dataTypeSlug != "" &&
		dataType.Slug != *dataTypeSlug {
		return nil, ErrDataTypeSlugMismatch
	}

	// Skip schema validation when there's no data to check, but still
	// return the resolved DataType so the resolver can derive slug.
	if input == nil {
		return dataType, nil
	}

	return dataType, v.validateInputWithSchema(input, *dataType)
}

// ValidateInputDataUniqueness validates input data uniqueness using a generic approach
func (v *Validator) ValidateInputDataUniqueness(ctx context.Context, executor QueryExecutor, params UniquenessValidationParams) error {
	if params.Input == nil {
		return nil
	}

	queries, argsList, fields, err := v.validateInputDataUniquenessQuery(ctx, params.Input, params.DataType, params.DbDriver, params.TableName, params.FieldName, params.ExcludeID)
	if err != nil {
		return err
	}

	for i, query := range queries {
		count, err := v.executeCountQuery(ctx, executor, query, argsList[i]...)
		if err != nil {
			return err
		}

		if count > 0 {
			return fmt.Errorf("%w: %v", ErrFieldNotUnique, fields[i])
		}
	}

	return nil
}

func (v *Validator) validateInputWithSchema(input map[string]any, dataType common_jsonschema.DataType) error {
	var filename string

	if dataType.Slug != "" {
		filename = fmt.Sprintf("%s.json", dataType.Slug)
	} else {
		filename = fmt.Sprintf("%s.json", dataType.ID.String())
	}

	compiler := jsonschema.NewCompiler()
	registerCustomFormats(compiler)
	compiler.AssertFormat()

	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(dataType.JsonSchema))
	if err != nil {
		return err
	}

	if err := compiler.AddResource(filename, doc); err != nil {
		return err
	}

	schema, err := compiler.Compile(filename)
	if err != nil {
		return err
	}

	return schema.Validate(input)
}

// validateInputDataUniquenessQuery checks if the schema contains fields marked as unique
// and returns one or more SQL queries to enforce uniqueness per field.
func (v *Validator) validateInputDataUniquenessQuery(
	ctx context.Context,
	input map[string]any,
	dataType *common_jsonschema.DataType,
	dbDriver,
	tableName,
	dataTypeColumn string,
	excludeID *uuid.UUID,
) ([]string, [][]any, []Field, error) {
	if input == nil {
		return nil, nil, nil, nil
	}

	uniqueFields, err := v.FindUniqueSchemaFields(dataType.JsonSchema)
	if err != nil {
		return nil, nil, nil, err
	}

	var (
		queries  []string
		argsList [][]any
		fields   []Field
	)

	for _, field := range uniqueFields {
		value, ok := v.jsonFindFieldValue(input, field)
		if !ok {
			continue
		}

		sqlStr, args, err := v.createQueryForCountingUniqueRecords(ctx, dbDriver, tableName, dataTypeColumn, field, value, dataType.ID, excludeID)
		if err != nil {
			return nil, nil, nil, err
		}

		queries = append(queries, sqlStr)
		argsList = append(argsList, args)
		fields = append(fields, field)
	}

	return queries, argsList, fields, nil
}

// FindUniqueFields parses the schema and detects all fields with "unique": true.
func (v *Validator) FindUniqueSchemaFields(jsonSchema string) ([]Field, error) {
	if len(jsonSchema) == 0 || !strings.Contains(jsonSchema, `"unique"`) {
		return nil, nil
	}

	var schemaMap map[string]any
	if err := json.Unmarshal([]byte(jsonSchema), &schemaMap); err != nil {
		return nil, err
	}

	return v.findUniqueFields(schemaMap, "", nil), nil
}

// findUniqueFields recursively searches for all fields with "unique": true.
func (v *Validator) findUniqueFields(schema map[string]any, name string, parent *Field) []Field {
	var (
		field        Field
		uniqueFields []Field
	)

	field.Parent = parent
	field.Name = name

	if fieldType, ok := schema["type"].(string); ok {
		field.Type = fieldType
	}

	if fieldUnique, ok := schema["unique"].(bool); ok && fieldUnique {
		field.Unique = true
		uniqueFields = append(uniqueFields, field)
	}

	if properties, ok := schema["properties"].(map[string]any); ok {
		for name := range properties {
			if schema, ok := properties[name].(map[string]any); ok {
				uniqueFields = append(uniqueFields, v.findUniqueFields(schema, name, &field)...)
			}
		}
	}

	return uniqueFields
}

// jsonFindFieldValue recursively searches the input to extract the value of the field with the given name.
func (v *Validator) jsonFindFieldValue(data map[string]any, field Field) (any, bool) {
	var value any = data

	for _, part := range field.Path() {
		node, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}

		value, ok = node[part]
		if !ok {
			return nil, false
		}
	}

	return value, true
}

// createQueryForCountingUniqueRecords builds a SQL query to check if a field value already exists.
func (v *Validator) createQueryForCountingUniqueRecords(
	ctx context.Context,
	dbDriver, table, jsonColumn string,
	uniqueField Field,
	fieldValue any,
	dataTypeID uuid.UUID,
	excludeID *uuid.UUID,
) (string, []any, error) {
	// TODO(michael): The tenant IDs should be part of the function signature.
	// Directly accessing the context here means we have to make assumptions
	// which operation is being performed and in which context. This is prone to
	// errors and unnecessarily hard to test. This function is indirectly called
	// from the create/update mutations, which already know exactly which
	// TenantID they operate on.
	req := request.ForContext(ctx)

	if !db.IsSafeIdentifier(table) {
		return "", nil, fmt.Errorf("%w: %q", ErrInvalidTable, table)
	}

	if !db.IsSafeIdentifier(jsonColumn) {
		return "", nil, fmt.Errorf("%w: %q", ErrInvalidJSONColumn, jsonColumn)
	}

	for _, part := range uniqueField.Path() {
		if !db.IsSafeIdentifier(part) {
			return "", nil, fmt.Errorf("%w: %q", ErrInvalidJSONField, uniqueField.String())
		}
	}

	var (
		cond string
		args []any
		ph   int
	)

	// nextPlaceholder returns the bind placeholder for the next argument in the
	// dialect's positional style. PostgreSQL uses $1, $2, …; SQLite (and the
	// empty test dialect) use ?. Postgres must not use ? — it collides with the
	// jsonb ? existence operator and the driver does not rebind raw SQL, so a
	// literal ? produces a syntax error before the placeholder is ever bound.
	nextPlaceholder := func() string {
		ph++
		if dbDriver == dialect.Postgres {
			return fmt.Sprintf("$%d", ph)
		}
		return "?"
	}

	switch dbDriver {
	case dialect.SQLite, "": // dialect is not set during tests
		// For SQLite, we use the json_extract function with a dot-separated path.
		path := strings.Join(uniqueField.Path(), ".")
		cond = fmt.Sprintf("json_extract(%s, '$.%s') = %s", jsonColumn, path, nextPlaceholder())
		args = append(args, fieldValue)

	case dialect.Postgres:
		// For PostgreSQL, we build a chain of -> and ->> operators, wrapping each
		// key in single quotes. Every part but the last uses -> to return a JSONB
		// object for further traversal; the last uses ->> to return text.
		parts := uniqueField.Path()
		ops := make([]string, len(parts))
		for i, part := range parts {
			if i == len(parts)-1 {
				ops[i] = fmt.Sprintf("->> '%s'", part)
			} else {
				ops[i] = fmt.Sprintf("-> '%s'", part)
			}
		}

		cond = fmt.Sprintf("(%s::jsonb %s) = %s", jsonColumn, strings.Join(ops, " "), nextPlaceholder())
		args = append(args, fieldValue)
	default:
		return "", nil, fmt.Errorf("%w: %q", ErrUnsupportedDialect, dbDriver)
	}

	// build query with placeholders
	whereClause := fmt.Sprintf("%s AND data_type_id = %s AND tenant_id = %s", cond, nextPlaceholder(), nextPlaceholder())
	args = append(args, dataTypeID, req.MutationTenantID())
	if excludeID != nil {
		whereClause += fmt.Sprintf(" AND id != %s", nextPlaceholder())
		args = append(args, *excludeID)
	}

	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", table, whereClause)

	return query, args, nil
}

func (v *Validator) executeCountQuery(ctx context.Context, executor QueryExecutor, query string, args ...any) (int, error) {
	count := 0

	// Execute custom query
	rows, err := executor.QueryContext(ctx, query, args...)
	if err != nil {
		return count, err
	}
	defer rows.Close()

	// Read custom query
	for rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return count, err
		}
	}
	if rows.Err() != nil {
		return count, rows.Err()
	}

	return count, nil
}
