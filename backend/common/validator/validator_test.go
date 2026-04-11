package validator_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common_jsonschema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

// Shared test data - define once at module level
var (
	testTenantID = uuid.MustParse("60ad2119-9b7f-498a-b99e-e8e340fc7fd7")

	testDataType1 = common_jsonschema.DataType{
		ID:         uuid.MustParse("4abbcec7-8709-405d-99d7-2b7f9323de91"),
		Slug:       "item",
		TenantID:   testTenantID,
		JsonSchema: string(test.MustLoadSchemaByName("item")),
	}

	testDataType2 = common_jsonschema.DataType{
		ID:         uuid.MustParse("4535afee-db12-46e0-8c50-43b58b8253a0"),
		Slug:       "customer",
		TenantID:   testTenantID,
		JsonSchema: string(test.MustLoadSchemaByName("customer")),
	}

	testDataTypeWithUniqueName = common_jsonschema.DataType{
		ID:         uuid.MustParse("26d0095c-a75b-4772-8734-287523ced169"),
		Slug:       "item-unique-name",
		TenantID:   testTenantID,
		JsonSchema: string(test.MustLoadSchemaByName("item_unique_name")),
	}

	testAllTestDataTypes = []common_jsonschema.DataType{
		testDataType1,
		testDataType2,
		testDataTypeWithUniqueName,
	}

	testItemInput = map[string]any{
		"type": "custom",
		"sum":  15,
		"meta": map[string]any{
			"name":   "TestItem",
			"weight": 50,
			"tags":   []any{"test", "validation"},
		},
	}
)

func TestValidator_ReadBySlug(t *testing.T) {
	tests := []struct {
		name          string
		slug          string
		mockDataTypes []common_jsonschema.DataType
		expectedError bool
	}{
		{
			name:          "successful read by slug",
			slug:          "item",
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: false,
		},
		{
			name:          "datatype not found",
			slug:          "non-existent",
			mockDataTypes: []common_jsonschema.DataType{},
			expectedError: true,
		},
		{
			name:          "multiple datatypes, correct one found",
			slug:          "item-unique-name",
			mockDataTypes: testAllTestDataTypes,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock provider
			mockProvider := &mocks.MockDataTypeProvider{}
			mockProvider.AddDataType(tt.mockDataTypes...)

			// Create validator
			v := validator.NewValidator(mockProvider)

			// Execute test
			result, err := v.ReadBySlug(t.Context(), tt.slug)

			// Assertions
			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, result)
				assert.Equal(t, validator.ErrDataTypeNotFound, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.slug, result.Slug)

			// Find expected result from mock data
			var expected *common_jsonschema.DataType
			for _, dt := range tt.mockDataTypes {
				if dt.Slug == tt.slug {
					expected = &dt
					break
				}
			}
			require.NotNil(t, expected, "Expected datatype not found in mock data")
			assert.Equal(t, expected.ID, result.ID)
			assert.Equal(t, expected.JsonSchema, result.JsonSchema)
		})
	}
}

func TestValidator_ReadByID(t *testing.T) {
	tests := []struct {
		name          string
		id            uuid.UUID
		mockDataTypes []common_jsonschema.DataType
		expectedError bool
	}{
		{
			name:          "successful read by ID",
			id:            testDataType1.ID,
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: false,
		},
		{
			name:          "datatype not found",
			id:            uuid.Max,
			mockDataTypes: []common_jsonschema.DataType{},
			expectedError: true,
		},
		{
			name:          "multiple datatypes, correct one found",
			id:            testDataType2.ID,
			mockDataTypes: testAllTestDataTypes,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock provider
			mockProvider := &mocks.MockDataTypeProvider{}
			mockProvider.AddDataType(tt.mockDataTypes...)

			// Create validator
			v := validator.NewValidator(mockProvider)

			// Execute test
			result, err := v.ReadByID(t.Context(), tt.id)

			// Assertions
			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, result)
				assert.Equal(t, validator.ErrDataTypeNotFound, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.id, result.ID)

			// Find expected result from mock data
			var expected *common_jsonschema.DataType
			for _, dt := range tt.mockDataTypes {
				if dt.ID == tt.id {
					expected = &dt
					break
				}
			}
			require.NotNil(t, expected, "Expected datatype not found in mock data")
			assert.Equal(t, expected.Slug, result.Slug)
			assert.Equal(t, expected.JsonSchema, result.JsonSchema)
		})
	}
}

func TestValidator_ValidateDataTypeInput(t *testing.T) {
	tests := []struct {
		name          string
		strict        bool
		input         map[string]any
		dataTypeID    uuid.UUID
		dataTypeSlug  string
		mockDataTypes []common_jsonschema.DataType
		expectedError bool
	}{
		{
			name:          "nil input returns nil",
			strict:        false,
			input:         nil,
			dataTypeID:    uuid.Nil,
			dataTypeSlug:  "",
			mockDataTypes: []common_jsonschema.DataType{},
			expectedError: false,
		},
		{
			name:          "valid input with datatype slug",
			strict:        false,
			input:         testItemInput,
			dataTypeID:    uuid.Nil,
			dataTypeSlug:  testDataType1.Slug,
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: false,
		},
		{
			name:          "valid input with datatype ID",
			strict:        false,
			input:         testItemInput,
			dataTypeID:    testDataType1.ID,
			dataTypeSlug:  "",
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: false,
		},
		{
			name:          "invalid input fails validation",
			strict:        false,
			input:         map[string]any{"type": "custom"}, // missing required "sum" and "meta"
			dataTypeID:    testDataType1.ID,
			dataTypeSlug:  "",
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: true,
		},
		{
			name:          "strict mode with no datatype returns error",
			strict:        true,
			input:         testItemInput,
			dataTypeID:    uuid.Nil,
			dataTypeSlug:  "",
			mockDataTypes: []common_jsonschema.DataType{},
			expectedError: true,
		},
		{
			name:          "non-strict mode with no datatype succeeds",
			strict:        false,
			input:         testItemInput,
			dataTypeID:    uuid.Nil,
			dataTypeSlug:  "",
			mockDataTypes: []common_jsonschema.DataType{},
			expectedError: false,
		},
		{
			name:          "datatype not found by slug",
			strict:        false,
			input:         testItemInput,
			dataTypeID:    uuid.Nil,
			dataTypeSlug:  "non-existent-slug",
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: true,
		},
		{
			name:          "datatype not found by ID",
			strict:        false,
			input:         testItemInput,
			dataTypeID:    uuid.Max,
			dataTypeSlug:  "",
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: true,
		},
		{
			name:   "valid customer input with different schema",
			strict: false,
			input: map[string]any{
				"fields": map[string]any{
					"eb214d08-6327-4b90-8143-4fa7b8ba1be3": "John Doe Customer",
				},
			},
			dataTypeID:    testDataType2.ID,
			dataTypeSlug:  "",
			mockDataTypes: []common_jsonschema.DataType{testDataType2},
			expectedError: false,
		},
		{
			name:          "empty slug is ignored when provided",
			strict:        false,
			input:         testItemInput,
			dataTypeID:    testDataType1.ID,
			dataTypeSlug:  "", // Empty slug should be ignored
			mockDataTypes: []common_jsonschema.DataType{testDataType1},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock provider
			mockProvider := &mocks.MockDataTypeProvider{}
			mockProvider.AddDataType(tt.mockDataTypes...)

			// Create validator
			v := validator.NewValidator(mockProvider)

			// Convert back to pointers for the API call
			var dataTypeID *uuid.UUID
			var dataTypeSlug *string

			if tt.dataTypeID != uuid.Nil {
				dataTypeID = &tt.dataTypeID
			}
			if tt.dataTypeSlug != "" {
				dataTypeSlug = &tt.dataTypeSlug
			}

			// Execute test
			result, err := v.ValidateDataTypeInput(t.Context(), tt.strict, tt.input, dataTypeID, dataTypeSlug)

			// Assertions - early return pattern
			if tt.expectedError {
				assert.Error(t, err)
				if tt.input == nil {
					assert.Nil(t, result)
				}
				return
			}

			require.NoError(t, err)

			if tt.input == nil {
				assert.Nil(t, result)
				return
			}

			// For successful validation with input
			if tt.dataTypeID == uuid.Nil && tt.dataTypeSlug == "" {
				// In non-strict mode with no datatype, result should be nil
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)

			// Verify the returned datatype matches expectations
			if tt.dataTypeID != uuid.Nil {
				assert.Equal(t, tt.dataTypeID, result.ID)
				return
			}

			if tt.dataTypeSlug != "" {
				assert.Equal(t, tt.dataTypeSlug, result.Slug)
			}
		})
	}
}

// setupTestEnvironment creates an isolated SQLite database for testing
func setupTestEnvironment(t *testing.T) *sql.DB {
	t.Helper()

	// Create an in-memory SQLite database for testing
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	// Create a test table that matches the expected structure
	_, err = db.Exec(`
		CREATE TABLE test_items (
			id TEXT PRIMARY KEY,
			data_type_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			data TEXT NOT NULL
		)
	`)
	require.NoError(t, err)

	return db
}

func TestValidator_ValidateInputDataUniqueness(t *testing.T) {
	t.Parallel()

	// Schema definitions
	schemaWithUniqueEmail := `{
		"type": "object",
		"properties": {
			"email": {"type": "string", "unique": true},
			"name": {"type": "string"}
		}
	}`

	schemaWithMultipleUnique := `{
		"type": "object",
		"properties": {
			"email": {"type": "string", "unique": true},
			"username": {"type": "string", "unique": true},
			"name": {"type": "string"}
		}
	}`

	schemaNoUnique := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age": {"type": "integer"}
		}
	}`

	schemaWithNestedUnique := `{
		"type": "object",
		"properties": {
			"user": {
				"type": "object",
				"properties": {
					"email": {"type": "string", "unique": true},
					"name": {"type": "string"}
				}
			}
		}
	}`

	tests := []struct {
		name          string
		setupData     func(db *sql.DB) // Function to insert test data
		params        validator.UniquenessValidationParams
		expectedError bool
		errorContains string
	}{
		{
			name:      "nil input returns nil",
			setupData: func(db *sql.DB) {}, // No setup needed
			params: validator.UniquenessValidationParams{
				Input:     nil,
				DataType:  &testDataType1,
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: false,
		},
		{
			name:      "schema with no unique fields returns nil",
			setupData: func(db *sql.DB) {}, // No setup needed
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"name": "John Doe",
					"age":  30,
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaNoUnique,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: false,
		},
		{
			name: "unique field with no duplicate found - success",
			setupData: func(db *sql.DB) {
				// Insert a record with different email
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, "existing-1", testDataType1.ID.String(), testTenantID.String(),
					`{"email": "different@example.com", "name": "Existing User"}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"email": "test@example.com",
					"name":  "John Doe",
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithUniqueEmail,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: false,
		},
		{
			name: "unique field with duplicate found - failure",
			setupData: func(db *sql.DB) {
				// Insert a record with the same email we'll test
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, "existing-2", testDataType1.ID.String(), testTenantID.String(),
					`{"email": "existing@example.com", "name": "Existing User"}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"email": "existing@example.com",
					"name":  "Jane Doe",
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithUniqueEmail,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: true,
			errorContains: "field value unique constraint violated",
		},
		{
			name: "multiple unique fields - first field duplicate",
			setupData: func(db *sql.DB) {
				// Insert a record with duplicate email but different username
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, "existing-3", testDataType1.ID.String(), testTenantID.String(),
					`{"email": "duplicate@example.com", "username": "uniqueuser", "name": "Existing User"}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"email":    "duplicate@example.com",
					"username": "newuser",
					"name":     "New User",
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithMultipleUnique,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: true,
			errorContains: "field value unique constraint violated",
		},
		{
			name: "multiple unique fields - second field duplicate",
			setupData: func(db *sql.DB) {
				// Insert a record with unique email but duplicate username
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, "existing-4", testDataType1.ID.String(), testTenantID.String(),
					`{"email": "unique@example.com", "username": "duplicateuser", "name": "Existing User"}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"email":    "new@example.com",
					"username": "duplicateuser",
					"name":     "New User",
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithMultipleUnique,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: true,
			errorContains: "field value unique constraint violated",
		},
		{
			name: "multiple unique fields - both fields unique - success",
			setupData: func(db *sql.DB) {
				// Insert a record with different email and username
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, "existing-5", testDataType1.ID.String(), testTenantID.String(),
					`{"email": "different@example.com", "username": "differentuser", "name": "Existing User"}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"email":    "new@example.com",
					"username": "newuser",
					"name":     "New User",
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithMultipleUnique,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: false,
		},
		{
			name: "nested unique field with duplicate found - failure",
			setupData: func(db *sql.DB) {
				// Insert a record with nested email
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, "existing-6", testDataType1.ID.String(), testTenantID.String(),
					`{"user": {"email": "nested@example.com", "name": "Nested User"}}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"user": map[string]any{
						"email": "nested@example.com",
						"name":  "Another User",
					},
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithNestedUnique,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: true,
			errorContains: "field value unique constraint violated",
		},
		{
			name: "ExcludeID parameter excludes current record",
			setupData: func(db *sql.DB) {
				// Insert a record with the same email we'll test (this should be excluded)
				excludeUUID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, excludeUUID.String(), testDataType1.ID.String(), testTenantID.String(),
					`{"email": "update@example.com", "name": "Existing User"}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"email": "update@example.com",
					"name":  "Updated User",
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithUniqueEmail,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
				ExcludeID: func() *uuid.UUID {
					id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
					return &id
				}(),
			},
			expectedError: false,
		},
		{
			name: "different tenant ID - no collision",
			setupData: func(db *sql.DB) {
				// Insert a record with same email but different tenant
				differentTenantID := uuid.New()
				_, err := db.Exec(`
					INSERT INTO test_items (id, data_type_id, tenant_id, data)
					VALUES (?, ?, ?, ?)
				`, "different-tenant", testDataType1.ID.String(), differentTenantID.String(),
					`{"email": "tenant@example.com", "name": "Other Tenant User"}`)
				require.NoError(t, err)
			},
			params: validator.UniquenessValidationParams{
				Input: map[string]any{
					"email": "tenant@example.com",
					"name":  "Same Tenant User",
				},
				DataType: &common_jsonschema.DataType{
					ID:         testDataType1.ID,
					JsonSchema: schemaWithUniqueEmail,
				},
				TableName: "test_items",
				FieldName: "data",
				DbDriver:  "sqlite3",
			},
			expectedError: false,
		},
	}

	// Create a separate test function for the new deeply nested tests
	t.Run("TestValidator_ValidateInputDataUniqueness_MultipleDeeplyNested", func(t *testing.T) {
		t.Parallel()

		// Schema for multiple deeply nested unique fields
		schemaMultipleDeeplyNestedUnique := `{
			"type": "object",
			"properties": {
				"profile": {
					"type": "object",
					"properties": {
						"personal": {
							"type": "object",
							"properties": {
								"email": {"type": "string", "unique": true}
							}
						}
					}
				},
				"settings": {
					"type": "object",
					"properties": {
						"account": {
							"type": "object",
							"properties": {
								"username": {"type": "string", "unique": true}
							}
						}
					}
				}
			}
		}`

		nestedTests := []struct {
			name        string
			setupData   func(ctx context.Context, db *sql.DB)
			input       map[string]interface{}
			tenantID    string
			expectError bool
		}{
			{
				name: "multiple deeply nested unique fields - first field duplicate",
				setupData: func(ctx context.Context, db *sql.DB) {
					// Insert record with duplicate nested field
					_, err := db.ExecContext(ctx, `
						INSERT INTO test_items (id, data_type_id, tenant_id, data)
						VALUES (?, ?, ?, ?)`,
						"existing-id", testDataType1.ID.String(), testTenantID.String(), `{"profile": {"personal": {"email": "john@example.com"}}, "settings": {"account": {"username": "differentuser"}}}`)
					require.NoError(t, err)
				},
				input: map[string]interface{}{
					"profile": map[string]interface{}{
						"personal": map[string]interface{}{
							"email": "john@example.com", // duplicate
						},
					},
					"settings": map[string]interface{}{
						"account": map[string]interface{}{
							"username": "newuser", // unique
						},
					},
				},
				tenantID:    "test-tenant",
				expectError: true,
			},
			{
				name: "multiple deeply nested unique fields - second field duplicate",
				setupData: func(ctx context.Context, db *sql.DB) {
					// Insert record with duplicate nested field
					_, err := db.ExecContext(ctx, `
						INSERT INTO test_items (id, data_type_id, tenant_id, data)
						VALUES (?, ?, ?, ?)`,
						"existing-id-2", testDataType1.ID.String(), testTenantID.String(), `{"profile": {"personal": {"email": "different@example.com"}}, "settings": {"account": {"username": "johndoe"}}}`)
					require.NoError(t, err)
				},
				input: map[string]interface{}{
					"profile": map[string]interface{}{
						"personal": map[string]interface{}{
							"email": "newemail@example.com", // unique
						},
					},
					"settings": map[string]interface{}{
						"account": map[string]interface{}{
							"username": "johndoe", // duplicate
						},
					},
				},
				tenantID:    "test-tenant",
				expectError: true,
			},
			{
				name: "multiple deeply nested unique fields - both fields unique - success",
				setupData: func(ctx context.Context, db *sql.DB) {
					// Insert record with different values for both nested fields
					_, err := db.ExecContext(ctx, `
						INSERT INTO test_items (id, data_type_id, tenant_id, data)
						VALUES (?, ?, ?, ?)`,
						"existing-id-3", testDataType1.ID.String(), testTenantID.String(), `{"profile": {"personal": {"email": "existing@example.com"}}, "settings": {"account": {"username": "existinguser"}}}`)
					require.NoError(t, err)
				},
				input: map[string]interface{}{
					"profile": map[string]interface{}{
						"personal": map[string]interface{}{
							"email": "new@example.com", // unique
						},
					},
					"settings": map[string]interface{}{
						"account": map[string]interface{}{
							"username": "newuser", // unique
						},
					},
				},
				tenantID:    "test-tenant",
				expectError: false,
			},
		}

		for _, tt := range nestedTests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				// Setup isolated test environment using the same helper as other tests
				db := setupTestEnvironment(t)
				defer db.Close()

				// Create context with tenant ID
				ctx := tenant.Context(t.Context(), testTenantID)

				// Setup test data
				tt.setupData(ctx, db)

				// Create parameters for uniqueness validation
				params := validator.UniquenessValidationParams{
					Input: tt.input,
					DataType: &common_jsonschema.DataType{
						ID:         testDataType1.ID,
						JsonSchema: schemaMultipleDeeplyNestedUnique,
					},
					TableName: "test_items",
					FieldName: "data",
					DbDriver:  "sqlite3",
				}

				// Setup validator
				mockProvider := &mocks.MockDataTypeProvider{}
				v := validator.NewValidator(mockProvider)

				// Execute test
				err := v.ValidateInputDataUniqueness(ctx, db, params)

				// Assertions
				if tt.expectError {
					assert.Error(t, err)
					assert.Contains(t, err.Error(), "field value unique constraint violated")
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Enable parallel execution

			// Setup isolated test environment
			db := setupTestEnvironment(t)
			defer db.Close()

			// Setup test data
			tt.setupData(db)

			// Setup validator
			mockProvider := &mocks.MockDataTypeProvider{}
			v := validator.NewValidator(mockProvider)

			// Create context with tenant ID
			ctx := tenant.Context(t.Context(), testTenantID)

			// Execute test
			err := v.ValidateInputDataUniqueness(ctx, db, tt.params)

			// Assertions
			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			assert.NoError(t, err)
		})
	}
}

func TestValidator_FindUniqueFields(t *testing.T) {
	// Setup validator
	mockProvider := &mocks.MockDataTypeProvider{}
	v := validator.NewValidator(mockProvider)

	tests := []struct {
		name           string
		jsonSchema     string
		expectedFields []string
		expectedError  bool
	}{
		{
			name:           "empty schema returns nil",
			jsonSchema:     "",
			expectedFields: nil,
			expectedError:  false,
		},
		{
			name:           "schema without unique fields returns nil",
			jsonSchema:     `{"type": "object", "properties": {"name": {"type": "string"}}}`,
			expectedFields: nil,
			expectedError:  false,
		},
		{
			name:           "schema with one unique field",
			jsonSchema:     `{"type": "object", "properties": {"name": {"type": "string", "unique": true}}}`,
			expectedFields: []string{"name"},
			expectedError:  false,
		},
		{
			name: "schema with multiple unique fields",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"email": {"type": "string", "unique": true},
					"username": {"type": "string", "unique": true},
					"age": {"type": "integer"}
				}
			}`,
			expectedFields: []string{"email", "username"},
			expectedError:  false,
		},
		{
			name: "schema with nested unique fields",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"user": {
						"type": "object",
						"properties": {
							"email": {"type": "string", "unique": true}
						}
					}
				}
			}`,
			expectedFields: []string{"user.email"},
			expectedError:  false,
		},
		{
			name: "schema with unique false is ignored",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"name": {"type": "string", "unique": false},
					"email": {"type": "string", "unique": true}
				}
			}`,
			expectedFields: []string{"email"},
			expectedError:  false,
		},
		{
			name: "schema with two levels deep nested unique fields",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"user": {
						"type": "object",
						"properties": {
							"profile": {
								"type": "object",
								"properties": {
									"email": {"type": "string", "unique": true},
									"phone": {"type": "string", "unique": true}
								}
							}
						}
					}
				}
			}`,
			expectedFields: []string{
				"user.profile.email",
				"user.profile.phone",
			},
			expectedError: false,
		},
		{
			name: "schema with three levels deep nested unique fields",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"company": {
						"type": "object",
						"properties": {
							"department": {
								"type": "object",
								"properties": {
									"manager": {
										"type": "object",
										"properties": {
											"employeeId": {"type": "string", "unique": true},
											"socialSecurityNumber": {"type": "string", "unique": true}
										}
									}
								}
							}
						}
					}
				}
			}`,
			expectedFields: []string{
				"company.department.manager.employeeId",
				"company.department.manager.socialSecurityNumber",
			},
			expectedError: false,
		},
		{
			name: "schema with mixed depth unique fields",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"username": {"type": "string", "unique": true},
					"user": {
						"type": "object",
						"properties": {
							"email": {"type": "string", "unique": true},
							"profile": {
								"type": "object",
								"properties": {
									"personalId": {"type": "string", "unique": true},
									"settings": {
										"type": "object",
										"properties": {
											"apiKey": {"type": "string", "unique": true}
										}
									}
								}
							}
						}
					}
				}
			}`,
			expectedFields: []string{
				"username",
				"user.email",
				"user.profile.personalId",
				"user.profile.settings.apiKey",
			},
			expectedError: false,
		},
		{
			name: "schema with four levels deep nested unique field",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"organization": {
						"type": "object",
						"properties": {
							"division": {
								"type": "object",
								"properties": {
									"team": {
										"type": "object",
										"properties": {
											"lead": {
												"type": "object",
												"properties": {
													"badgeNumber": {"type": "string", "unique": true}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}`,
			expectedFields: []string{
				"organization.division.team.lead.badgeNumber",
			},
			expectedError: false,
		},
		{
			name: "schema with multiple branches of nested unique fields",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"primary": {
						"type": "object",
						"properties": {
							"contact": {
								"type": "object",
								"properties": {
									"email": {"type": "string", "unique": true}
								}
							}
						}
					},
					"secondary": {
						"type": "object",
						"properties": {
							"contact": {
								"type": "object",
								"properties": {
									"phone": {"type": "string", "unique": true}
								}
							}
						}
					},
					"tertiary": {
						"type": "object",
						"properties": {
							"details": {
								"type": "object",
								"properties": {
									"reference": {
										"type": "object",
										"properties": {
											"code": {"type": "string", "unique": true}
										}
									}
								}
							}
						}
					}
				}
			}`,
			expectedFields: []string{
				"primary.contact.email",
				"secondary.contact.phone",
				"tertiary.details.reference.code",
			},
			expectedError: false,
		},
		{
			name: "schema with deeply nested non-unique and unique fields mixed",
			jsonSchema: `{
				"type": "object",
				"properties": {
					"level1": {
						"type": "object",
						"properties": {
							"name": {"type": "string", "unique": false},
							"level2": {
								"type": "object",
								"properties": {
									"description": {"type": "string"},
									"level3": {
										"type": "object",
										"properties": {
											"id": {"type": "string", "unique": true},
											"level4": {
												"type": "object",
												"properties": {
													"timestamp": {"type": "string"},
													"uniqueHash": {"type": "string", "unique": true}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}`,
			expectedFields: []string{
				"level1.level2.level3.id",
				"level1.level2.level3.level4.uniqueHash",
			},
			expectedError: false,
		},
		{
			name:           "invalid JSON schema returns error",
			jsonSchema:     `{"type": "object", "properties": {"name": {"type": "string", "unique": true}`, // missing closing braces
			expectedFields: nil,
			expectedError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Execute test
			result, err := v.FindUniqueSchemaFields(tt.jsonSchema)

			// Assertions - early return pattern
			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)

			if tt.expectedFields == nil {
				assert.Nil(t, result)
				return
			}

			fields := make([]string, len(result))
			for i, fp := range result {
				fields[i] = fp.String()
			}

			require.NotNil(t, result)
			assert.ElementsMatch(t, tt.expectedFields, fields, "Case: %s", tt.name)
		})
	}
}
