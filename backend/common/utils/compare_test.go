package utils_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockPublisher struct {
	sentEvents  []*events.UpdateEventMessage
	returnError error
	mu          sync.Mutex
}

type testStruct struct {
	Name  string
	Age   int
	Email string
	City  string
}

type testCase struct {
	name            string
	oldObj          interface{}
	newObj          interface{}
	jsonDataName    string
	expectedChanges map[string]struct {
		old any
		new any
	}
	expectError    bool
	publisherError error
}

func TestSendUpdatedFieldsEvents(t *testing.T) {
	testCases := []testCase{
		{
			name:         "no updates",
			oldObj:       testStruct{Name: "John", Age: 30, Email: "john@example.com", City: "NYC"},
			newObj:       testStruct{Name: "John", Age: 30, Email: "john@example.com", City: "NYC"},
			jsonDataName: "",
			expectedChanges: map[string]struct {
				old any
				new any
			}{},
			expectError:    false,
			publisherError: nil,
		},
		{
			name:         "one field updated",
			oldObj:       testStruct{Name: "John", Age: 30, Email: "john@example.com", City: "NYC"},
			newObj:       testStruct{Name: "Jane", Age: 30, Email: "john@example.com", City: "NYC"},
			jsonDataName: "",
			expectedChanges: map[string]struct {
				old any
				new any
			}{
				"name": {old: "John", new: "Jane"},
			},
			expectError:    false,
			publisherError: nil,
		},
		{
			name:         "two fields updated",
			oldObj:       testStruct{Name: "John", Age: 30, Email: "john@example.com", City: "NYC"},
			newObj:       testStruct{Name: "Jane", Age: 31, Email: "john@example.com", City: "NYC"},
			jsonDataName: "",
			expectedChanges: map[string]struct {
				old any
				new any
			}{
				"name": {old: "John", new: "Jane"},
				"age":  {old: 30, new: 31},
			},
			expectError:    false,
			publisherError: nil,
		},
		{
			name:         "three fields updated",
			oldObj:       testStruct{Name: "John", Age: 30, Email: "john@example.com", City: "NYC"},
			newObj:       testStruct{Name: "Jane", Age: 31, Email: "jane@example.com", City: "NYC"},
			jsonDataName: "",
			expectedChanges: map[string]struct {
				old any
				new any
			}{
				"name":  {old: "John", new: "Jane"},
				"age":   {old: 30, new: 31},
				"email": {old: "john@example.com", new: "jane@example.com"},
			},
			expectError:    false,
			publisherError: nil,
		},
		{
			name:         "all fields updated",
			oldObj:       testStruct{Name: "John", Age: 30, Email: "john@example.com", City: "NYC"},
			newObj:       testStruct{Name: "Jane", Age: 31, Email: "jane@example.com", City: "LA"},
			jsonDataName: "",
			expectedChanges: map[string]struct {
				old any
				new any
			}{
				"name":  {old: "John", new: "Jane"},
				"age":   {old: 30, new: 31},
				"email": {old: "john@example.com", new: "jane@example.com"},
				"city":  {old: "NYC", new: "LA"},
			},
			expectError:    false,
			publisherError: nil,
		},
		{
			name:   "publisher error",
			oldObj: testStruct{Name: "John", Age: 30, Email: "john@example.com", City: "NYC"},
			newObj: testStruct{Name: "Jane", Age: 31, Email: "jane@example.com", City: "LA"},
			expectedChanges: map[string]struct {
				old any
				new any
			}{
				"name":  {old: "John", new: "Jane"},
				"age":   {old: 33, new: 31},
				"email": {old: "john@example.com", new: "jane@example.com"},
				"city":  {old: "NYC", new: "LA"},
			}, // We still expect these attempted changes but error triggers before counting
			expectError:    true,
			publisherError: assert.AnError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &mocks.MockPublisher{}

			publisher.On("SendUpdateEvent", mock.AnythingOfType("*events.UpdateEventMessage")).Return(tc.publisherError)
			defer publisher.AssertNumberOfCalls(t, "SendUpdateEvent", len(tc.expectedChanges))

			eventMessage := events.MutationEventMessage{
				Service:   "test",
				Operation: "update",
				Type:      "testType",
				Schema:    "testSchema",
				ID:        uuid.New(),
				TenantID:  uuid.New(),
			}

			err := utils.SendUpdatedFieldsEvents(t.Context(), publisher, eventMessage, tc.oldObj, tc.newObj, tc.jsonDataName)

			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.publisherError)
				return
			}

			require.NoError(t, err)
			require.Len(t, publisher.Calls, len(tc.expectedChanges))

			// build map from attribute to event message
			got := make(map[string]events.UpdateAttributeDetails, len(publisher.Calls))

			for _, call := range publisher.Calls {
				require.Equal(t, "SendUpdateEvent", call.Method)
				event := call.Arguments[0].(*events.UpdateEventMessage)
				details := event.Data.(events.UpdateAttributeDetails)
				got[event.Attribute] = details
			}

			for attr, change := range tc.expectedChanges {
				assert.NotNil(t, got[attr], "missing event for attribute %q", attr)
				assert.Equal(t, change.old, got[attr].OldValue, "old value mismatch for attribute %q", attr)
				assert.Equal(t, change.new, got[attr].NewValue, "new value mismatch for attribute %q", attr)
			}
		})
	}
}
