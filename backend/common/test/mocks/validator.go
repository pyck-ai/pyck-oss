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

func (m *MockDataTypeProvider) ValidateDataTypeInput(ctx context.Context, strict bool, input map[string]interface{}, dataTypeID *uuid.UUID, dataTypeSlug *string) (*json_schema.DataType, error) {
	if input == nil {
		return nil, nil
	}

	var dataType *json_schema.DataType
	if dataTypeSlug != nil && *dataTypeSlug != "" {
		dt, err := m.ReadBySlug(ctx, *dataTypeSlug)
		if err != nil {
			return nil, err
		}
		dataType = dt
	} else if dataTypeID != nil {
		dt, err := m.ReadByID(ctx, *dataTypeID)
		if err != nil {
			return nil, err
		}
		dataType = dt
	} else if strict {
		return nil, validator.ErrDataTypeNotSet
	}

	return dataType, nil
}

func (m *MockDataTypeProvider) ValidateInputDataUniqueness(ctx context.Context, executor validator.QueryExecutor, params validator.UniquenessValidationParams) error {
	return nil
}

func (m *MockDataTypeProvider) ReadBySlug(ctx context.Context, slug string) (*json_schema.DataType, error) {
	for _, dataType := range m.DataTypes {
		if dataType.Slug == slug {
			return &dataType, nil
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
