package mocks

import (
	"context"

	"github.com/google/uuid"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/validator"
)

type MockDataTypeProvider struct {
	DataTypes []json_schema.DataType
}

func (m *MockDataTypeProvider) AddDataType(dataTypes ...json_schema.DataType) {
	m.DataTypes = append(m.DataTypes, dataTypes...)
}

// MockDataTypeProvider implements validator.DataTypeReader (ReadByID +
// ReadBySlug). Tests wrap it via validator.NewValidator(provider), so the
// real Validator drives ValidateDataTypeInput / ValidateInputDataUniqueness —
// the mock only supplies the reads.

// ReadBySlug returns the most-recently-added matching non-deleted DataType
// (newest wins, mirroring the production cache + provider semantics).
func (m *MockDataTypeProvider) ReadBySlug(ctx context.Context, slug string) (*json_schema.DataType, error) {
	for i := len(m.DataTypes) - 1; i >= 0; i-- {
		dt := m.DataTypes[i]
		if dt.Slug == slug && dt.DeletedAt == nil {
			return &dt, nil
		}
	}

	return nil, validator.ErrDataTypeNotFound
}

func (m *MockDataTypeProvider) ReadByID(ctx context.Context, id uuid.UUID) (*json_schema.DataType, error) {
	for _, dataType := range m.DataTypes {
		if dataType.ID == id {
			return &dataType, nil
		}
	}

	return nil, validator.ErrDataTypeNotFound
}
