package utils

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/log"
	"golang.org/x/sync/errgroup"
)

type UpdatedField struct {
	OldValue any
	NewValue any
}

func GetUpdatedFields(oldObject, newObject any, jsonDataName string) (map[string]UpdatedField, error) {
	oldVal := reflect.ValueOf(oldObject)
	newVal := reflect.ValueOf(newObject)

	// Dereference pointers
	if oldVal.Kind() == reflect.Pointer {
		if oldVal.IsNil() {
			return map[string]UpdatedField{}, nil
		}
		oldVal = oldVal.Elem()
	}
	if newVal.Kind() == reflect.Pointer {
		if newVal.IsNil() {
			return map[string]UpdatedField{}, nil
		}
		newVal = newVal.Elem()
	}

	if oldVal.Kind() != reflect.Struct || newVal.Kind() != reflect.Struct {
		return nil, fmt.Errorf("parameters must be structs")
	}

	if oldVal.Type() != newVal.Type() {
		return nil, fmt.Errorf("parameters must be of the same type")
	}

	updatedFields := make(map[string]UpdatedField)
	t := oldVal.Type()
	for i := 0; i < oldVal.NumField(); i++ {
		sf := t.Field(i)

		// Skip unexported fields (PkgPath set for unexported identifiers)
		if sf.PkgPath != "" { // unexported
			continue
		}

		fieldName := sf.Name
		ovField := oldVal.Field(i)
		nvField := newVal.Field(i)

		// Ensure we can safely interface (embedded unexported, etc.)
		if !ovField.CanInterface() || !nvField.CanInterface() {
			continue
		}

		if len(jsonDataName) > 0 && fieldName == jsonDataName {
			// Only process if both are maps[string]any
			oldMap, okOld := ovField.Interface().(map[string]any)
			newMap, okNew := nvField.Interface().(map[string]any)
			if okOld && okNew {
				if !reflect.DeepEqual(oldMap, newMap) {
					updatedFields[fieldName] = UpdatedField{
						OldValue: getUpdatedJsonData(newMap, oldMap),
						NewValue: getUpdatedJsonData(oldMap, newMap),
					}
				}
			}
			continue
		}

		oldField := ovField.Interface()
		newField := nvField.Interface()
		if !reflect.DeepEqual(oldField, newField) {
			updatedFields[fieldName] = UpdatedField{OldValue: oldField, NewValue: newField}
		}
	}

	return updatedFields, nil
}

func getUpdatedJsonData(oldMap, newMap map[string]any) map[string]any {
	updatedData := make(map[string]any)

	for key, oldValue := range oldMap {
		if newValue, exists := newMap[key]; exists {
			if !reflect.DeepEqual(oldValue, newValue) {
				updatedData[key] = newValue
			}
		} else {
			updatedData[key] = nil
		}
	}

	for key, newValue := range newMap {
		if _, exists := oldMap[key]; !exists {
			updatedData[key] = newValue
		}
	}

	return updatedData
}

func SendUpdatedFieldsEvents(ctx context.Context, publisher events.Publisher, eventMessage events.MutationEventMessage, oldObject, newObject any, jsonDataName string) error {
	updatedFields, err := GetUpdatedFields(oldObject, newObject, jsonDataName)
	if err != nil {
		return err
	}

	g := &errgroup.Group{}

	for field, history := range updatedFields {
		g.Go(func() error {
			localField := field
			if localField == jsonDataName || localField == "JSONSchema" {
				localField = "json_data"
			}

			return publisher.SendUpdateEvent(ctx, &events.UpdateEventMessage{
				Service:   eventMessage.Service,
				Operation: eventMessage.Operation,
				Type:      eventMessage.Type,
				Schema:    eventMessage.Schema,
				ID:        eventMessage.ID,
				TenantID:  eventMessage.TenantID,
				Attribute: strings.ToLower(localField),
				Data: events.UpdateAttributeDetails{
					OldValue: history.OldValue,
					NewValue: history.NewValue,
				},
			})
		})
	}

	return g.Wait()
}

func SendUpdatedFieldsEventsAsync(ctx context.Context, publisher events.Publisher, eventMessage events.MutationEventMessage, oldObject, newObject any, jsonDataName string) {
	if feature.HasFeature(ctx, feature.FEATURE_SYNC_UPDATES) {
		// During testing, we force synchronous execution to make it easier to verify results
		_ = SendUpdatedFieldsEvents(ctx, publisher, eventMessage, oldObject, newObject, jsonDataName)
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.ForContext(ctx).Error().
					Interface("panic", r).
					Msg("panic recovered during update events")
			}
		}()

		err := SendUpdatedFieldsEvents(ctx, publisher, eventMessage, oldObject, newObject, jsonDataName)
		if err != nil {
			log.ForContext(ctx).Error().
				Err(err).
				Msg("error sending update events")
		}
	}()
}
