package mixin

import (
	"reflect"

	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
)

var (
	DataFieldDataTypeID   = "data_type_id"
	DataFieldDataTypeSlug = "data_type_slug"
	DataFieldData         = "data"
)

// DataMixin adds a data field with JSON schema support.
type DataMixin struct {
	mixin.Schema
}

func (DataMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID(DataFieldDataTypeID, uuid.UUID{}).
			Optional(),
		field.String(DataFieldDataTypeSlug).
			Optional(),
		field.JSON(DataFieldData, map[string]any{}).
			Optional().
			Annotations(entgql.Type("Map")),
	}
}

// PatchDataTypeIdSlugInput is a workaround to set data type ID and slug.
//
// Deprecated: This function should be removed after switching to use only slug.
func PatchDataTypeIdSlugInput(input any, dt *json_schema.DataType) {
	if input == nil || dt == nil {
		return
	}
	v := reflect.ValueOf(input)
	// If input is a pointer, get the element
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}

	// Set DataTypeID if the field exists and is settable
	if field := v.FieldByName("DataTypeID"); field.IsValid() && field.CanSet() {
		switch field.Type() {
		case reflect.TypeFor[uuid.UUID]():
			field.Set(reflect.ValueOf(dt.ID))
		case reflect.TypeFor[*uuid.UUID]():
			id := dt.ID
			field.Set(reflect.ValueOf(&id))
		}
	}

	// Set DataTypeSlug if the field exists and is settable
	if field := v.FieldByName("DataTypeSlug"); field.IsValid() && field.CanSet() {
		switch field.Type() {
		case reflect.TypeFor[string]():
			field.Set(reflect.ValueOf(dt.Slug))
		case reflect.TypeFor[*string]():
			slug := dt.Slug
			field.Set(reflect.ValueOf(&slug))
		}
	}
}
