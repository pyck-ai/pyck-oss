package events

import (
	"reflect"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/internal/fieldnames"
	"github.com/pyck-ai/pyck/backend/common/internal/searchattributes"
)

// BuildSearchAttributes creates workflow search attributes from an entity.
// It extracts fields by testing for interface implementations first,
// then falls back to reflection on struct fields:
//   - IDer: entity ID
//   - TenantIDer: tenant ID
//   - DataTypeSlugger: data type slug (or DataTypeSlug field via reflection)
//
// Fallback values for entityID and tenantID are used when the entity
// doesn't implement the corresponding interfaces.
func BuildSearchAttributes(service string, entity any, entityID, tenantID uuid.UUID) map[string]string {
	attrs := make(map[string]string, 5)
	attrs[searchattributes.PyckServiceKey] = service

	// Entity ID: try interface first, then fallback
	if ider, ok := entity.(IDer); ok {
		attrs[searchattributes.PyckDataIDKey] = ider.GetID().String()
	} else if entityID != uuid.Nil {
		attrs[searchattributes.PyckDataIDKey] = entityID.String()
	}

	// Tenant ID: try interface first, then fallback
	if ter, ok := entity.(TenantIDer); ok {
		if tid := ter.GetTenantID(); tid != uuid.Nil {
			attrs[searchattributes.PyckTenantIDKey] = tid.String()
		}
	} else if tenantID != uuid.Nil {
		attrs[searchattributes.PyckTenantIDKey] = tenantID.String()
	}

	// Data type slug: try interface first, then reflection on field
	if dts, ok := entity.(DataTypeSlugger); ok {
		if slug := dts.GetDataTypeSlug(); slug != "" {
			attrs[searchattributes.PyckDataTypeKey] = slug
		}
	} else if slug := extractDataTypeSlug(entity); slug != "" {
		attrs[searchattributes.PyckDataTypeKey] = slug
	}

	return attrs
}

// extractDataTypeSlug extracts the DataTypeSlug field from an entity using reflection.
// This is a fallback for Ent entities that don't implement DataTypeSlugger interface.
func extractDataTypeSlug(entity any) string {
	if entity == nil {
		return ""
	}

	rv := reflect.ValueOf(entity)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}

	// Try DataTypeSlug field (from DataMixin)
	field := rv.FieldByName(fieldnames.FieldDataTypeSlug.String())
	if !field.IsValid() {
		return ""
	}

	if slug, ok := field.Interface().(string); ok {
		return slug
	}

	return ""
}
