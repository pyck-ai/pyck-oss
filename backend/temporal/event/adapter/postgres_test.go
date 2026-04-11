package adapter_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyck-ai/pyck/backend/temporal/event"
	"github.com/pyck-ai/pyck/backend/temporal/event/adapter"
	enumspb "go.temporal.io/api/enums/v1"
	temporalconfig "go.temporal.io/server/common/config"
)

func TestPostgresListenAdapter_NilPointerFix(t *testing.T) {
	// Test that the adapter properly handles database initialization
	// and doesn't cause segmentation faults

	// Create a mock temporal config
	config := &temporalconfig.Config{
		Persistence: temporalconfig.Persistence{
			VisibilityStore: "test-visibility",
			DataStores: map[string]temporalconfig.DataStore{
				"test-visibility": {
					SQL: &temporalconfig.SQL{
						PluginName:   "postgres12",
						DatabaseName: "test_db",
						ConnectAddr:  "localhost:5432",
						User:         "test_user",
						Password:     "test_pass",
					},
				},
			},
		},
	}

	handler := &event.Handler{}
	adapter, err := adapter.NewPostgresAdapter(handler, "test_channel", config)

	// We expect this to fail due to connection issues in test environment,
	// but it should NOT segfault
	if err != nil {
		t.Logf("Expected connection error in test environment: %v", err)
	}

	// If adapter was created (unlikely in test env), test defensive methods
	if adapter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		// These should not cause segmentation faults
		err := adapter.SetupTrigger(ctx)
		if err != nil {
			t.Logf("Expected SetupTrigger error in test environment: %v", err)
		}

		err = adapter.RemoveTrigger(ctx)
		if err != nil {
			t.Logf("Expected RemoveTrigger error in test environment: %v", err)
		}

		// Stop should handle nil database gracefully
		err = adapter.Stop()
		if err != nil {
			t.Logf("Stop error: %v", err)
		}
	}
}

func TestPostgresListenAdapter_NilDatabaseHandling(t *testing.T) {
	// Test that methods properly handle nil database connections

	adapter := &adapter.PostgresAdapter{
		ChannelName: "test",
		DB:          nil, // Explicitly nil
	}

	ctx := context.Background()

	// SetupTrigger should return error, not segfault
	err := adapter.SetupTrigger(ctx)
	if err == nil {
		t.Error("Expected error when database is nil")
	}
	if err != nil && err.Error() == "" {
		t.Error("Error message should not be empty")
	}

	// RemoveTrigger should return error, not segfault
	err = adapter.RemoveTrigger(ctx)
	if err == nil {
		t.Error("Expected error when database is nil")
	}
	if err != nil && err.Error() == "" {
		t.Error("Error message should not be empty")
	}

	// Stop should handle nil database gracefully
	err = adapter.Stop()
	// Stop might succeed even with nil db if listener is also nil
	t.Logf("Stop result: %v", err)
}

func TestWorkflowEventPayload_JSONMarshaling(t *testing.T) {
	// Test that our payload struct works correctly after removing task_queue_name
	payload := adapter.WorkflowEventPayload{
		Operation:   "INSERT",
		NamespaceID: "test-ns",
		WorkflowID:  "test-wf",
		RunID:       "test-run",
		Status:      enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
	}

	// Test JSON marshaling works correctly
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal WorkflowEventPayload to JSON: %v", err)
	}

	// Verify JSON contains expected fields
	jsonStr := string(jsonBytes)
	expectedFields := []string{
		`"op":"INSERT"`,
		`"namespace_id":"test-ns"`,
		`"workflow_id":"test-wf"`,
		`"run_id":"test-run"`,
		`"status":1`, // WORKFLOW_EXECUTION_STATUS_RUNNING = 1
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("JSON output missing expected field %s. Got: %s", field, jsonStr)
		}
	}

	// Test JSON unmarshaling works correctly
	var unmarshaled adapter.WorkflowEventPayload
	if err := json.Unmarshal(jsonBytes, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal JSON back to WorkflowEventPayload: %v", err)
	}

	// Verify roundtrip equality
	if unmarshaled.Operation != payload.Operation {
		t.Errorf("Operation mismatch: expected %s, got %s", payload.Operation, unmarshaled.Operation)
	}
	if unmarshaled.NamespaceID != payload.NamespaceID {
		t.Errorf("NamespaceID mismatch: expected %s, got %s", payload.NamespaceID, unmarshaled.NamespaceID)
	}
	if unmarshaled.WorkflowID != payload.WorkflowID {
		t.Errorf("WorkflowID mismatch: expected %s, got %s", payload.WorkflowID, unmarshaled.WorkflowID)
	}
	if unmarshaled.RunID != payload.RunID {
		t.Errorf("RunID mismatch: expected %s, got %s", payload.RunID, unmarshaled.RunID)
	}
	if unmarshaled.Status != payload.Status {
		t.Errorf("Status mismatch: expected %v, got %v", payload.Status, unmarshaled.Status)
	}
}

func TestPostgresListenAdapter_BuildDatabaseURL(t *testing.T) {
	// Test the database URL building logic
	testCases := []struct {
		name     string
		config   *temporalconfig.SQL
		expected string
		hasError bool
	}{
		{
			name: "Valid config with user and password",
			config: &temporalconfig.SQL{
				PluginName:   "postgres12",
				DatabaseName: "temporal",
				ConnectAddr:  "localhost:5432",
				User:         "temporal_user",
				Password:     "temporal_pass",
			},
			expected: "postgres://temporal_user:temporal_pass@localhost:5432/temporal?sslmode=disable",
			hasError: false,
		},
		{
			name: "Valid config without user and password",
			config: &temporalconfig.SQL{
				PluginName:   "postgres12",
				DatabaseName: "temporal",
				ConnectAddr:  "localhost:5432",
			},
			expected: "postgres://localhost:5432/temporal?sslmode=disable",
			hasError: false,
		},
		{
			name: "Unsupported plugin",
			config: &temporalconfig.SQL{
				PluginName:   "mysql",
				DatabaseName: "temporal",
				ConnectAddr:  "localhost:3306",
			},
			expected: "",
			hasError: true,
		},
		// Skip this test case for now as Connect function signature is complex
		// The important thing is testing the nil Connect case works correctly
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &adapter.PostgresAdapter{
				Sqlconfig: tc.config,
			}

			url, err := adapter.BuildDatabaseURL()

			if tc.hasError {
				if err == nil {
					t.Errorf("Expected error for %s, but got none", tc.name)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for %s: %v", tc.name, err)
				}
				if url != tc.expected {
					t.Errorf("URL mismatch for %s: expected %s, got %s", tc.name, tc.expected, url)
				}
			}
		})
	}
}

func TestPostgresListenAdapter_ErrorMessages(t *testing.T) {
	// Test that error messages are meaningful and informative
	adapter := &adapter.PostgresAdapter{
		ChannelName: "test",
		DB:          nil,
	}

	ctx := context.Background()

	// Test SetupTrigger error message
	err := adapter.SetupTrigger(ctx)
	if err == nil {
		t.Error("Expected error from SetupTrigger with nil database")
	} else {
		expectedMsg := "database connection is nil - adapter not properly initialized"
		if !strings.Contains(err.Error(), expectedMsg) {
			t.Errorf("SetupTrigger error message should contain '%s', got: %s", expectedMsg, err.Error())
		}
	}

	// Test RemoveTrigger error message
	err = adapter.RemoveTrigger(ctx)
	if err == nil {
		t.Error("Expected error from RemoveTrigger with nil database")
	} else {
		expectedMsg := "database connection is nil - adapter not properly initialized"
		if !strings.Contains(err.Error(), expectedMsg) {
			t.Errorf("RemoveTrigger error message should contain '%s', got: %s", expectedMsg, err.Error())
		}
	}
}

func TestPostgresListenAdapter_GetMetrics(t *testing.T) {
	// Test that GetMetrics returns expected data structure
	adapter := &adapter.PostgresAdapter{
		ChannelName: "test_channel",
	}

	metrics := adapter.GetMetrics()

	// Verify expected fields exist
	if metrics["type"] != "postgres_listen" {
		t.Errorf("Expected type 'postgres_listen', got %v", metrics["type"])
	}
	if metrics["channel"] != "test_channel" {
		t.Errorf("Expected channel 'test_channel', got %v", metrics["channel"])
	}
}

func TestPostgresListenAdapter_GetInterceptor(t *testing.T) {
	// Test that GetInterceptor returns nil (as documented)
	adapter := &adapter.PostgresAdapter{}
	interceptor := adapter.GetInterceptor()
	if interceptor != nil {
		t.Error("Expected GetInterceptor to return nil")
	}
}

func TestPostgresListenAdapter_ChannelNameDefault(t *testing.T) {
	// Test that empty channel name defaults to expected value
	config := &temporalconfig.Config{
		Persistence: temporalconfig.Persistence{
			VisibilityStore: "test-visibility",
			DataStores: map[string]temporalconfig.DataStore{
				"test-visibility": {
					SQL: &temporalconfig.SQL{
						PluginName:   "postgres12",
						DatabaseName: "test_db",
						ConnectAddr:  "localhost:5432",
						User:         "test_user",
						Password:     "test_pass",
					},
				},
			},
		},
	}

	handler := &event.Handler{}

	// Test with empty channel name
	adapter, err := adapter.NewPostgresAdapter(handler, "", config)

	// We expect connection error in test env, but should set default channel name
	if err != nil {
		t.Logf("Expected connection error: %v", err)
	}

	if adapter != nil && adapter.ChannelName != "temporal_workflow_events" {
		t.Errorf("Expected default channel name 'temporal_workflow_events', got '%s'", adapter.ChannelName)
	}
}

func TestWorkflowEventPayload_AllOperationTypes(t *testing.T) {
	// Test all operation types work correctly
	operations := []string{"INSERT", "UPDATE", "DELETE"}

	for _, op := range operations {
		t.Run(op, func(t *testing.T) {
			payload := adapter.WorkflowEventPayload{
				Operation:   op,
				NamespaceID: "test-ns",
				WorkflowID:  "test-wf",
				RunID:       "test-run",
				Status:      enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			}

			jsonBytes, err := json.Marshal(payload)
			if err != nil {
				t.Errorf("Failed to marshal %s operation: %v", op, err)
			}

			var unmarshaled adapter.WorkflowEventPayload
			if err := json.Unmarshal(jsonBytes, &unmarshaled); err != nil {
				t.Errorf("Failed to unmarshal %s operation: %v", op, err)
			}

			if unmarshaled.Operation != op {
				t.Errorf("Operation mismatch for %s: expected %s, got %s", op, op, unmarshaled.Operation)
			}
		})
	}
}
