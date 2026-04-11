package authn_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test ComputeUUID function
func TestComputeUUID(t *testing.T) {
	t.Parallel()

	// Test deterministic behavior - same input should always produce same output
	t.Run("deterministic generation", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			namespace string
			value     string
		}{
			{"https://auth.test.pyck.cloud", "123456789"},
			{"https://auth.prod.pyck.cloud", "987654321"},
			{"", "123456789"},                    // empty namespace
			{"https://auth.test.pyck.cloud", ""}, // empty value
			{"", ""},                             // both empty
		}

		for _, tc := range testCases {
			// Generate UUID multiple times
			uuid1 := authn.ComputeUUID(tc.namespace, tc.value)
			uuid2 := authn.ComputeUUID(tc.namespace, tc.value)
			uuid3 := authn.ComputeUUID(tc.namespace, tc.value)

			// All should be identical
			assert.Equal(t, uuid1, uuid2, "UUID should be deterministic for namespace=%q, value=%q", tc.namespace, tc.value)
			assert.Equal(t, uuid2, uuid3, "UUID should be deterministic for namespace=%q, value=%q", tc.namespace, tc.value)

			// Should not be nil
			assert.NotEqual(t, uuid.Nil, uuid1, "UUID should not be nil for namespace=%q, value=%q", tc.namespace, tc.value)
		}
	})

	// Test that different inputs produce different UUIDs
	t.Run("uniqueness", func(t *testing.T) {
		t.Parallel()

		uuids := map[string]uuid.UUID{
			"ns1_val1":  authn.ComputeUUID("ns1", "val1"),
			"ns1_val2":  authn.ComputeUUID("ns1", "val2"),
			"ns2_val1":  authn.ComputeUUID("ns2", "val1"),
			"ns2_val2":  authn.ComputeUUID("ns2", "val2"),
			"empty_val": authn.ComputeUUID("", "val1"),
			"ns1_empty": authn.ComputeUUID("ns1", ""),
		}

		// Verify all UUIDs are unique
		seen := make(map[uuid.UUID]string)
		for key, id := range uuids {
			if existingKey, exists := seen[id]; exists {
				t.Errorf("UUID collision: %s and %s both produced %v", key, existingKey, id)
			}
			seen[id] = key
		}
	})

	// Test known values for backward compatibility
	// These values were generated with the current implementation and should remain stable
	// If any of these tests fail, it means the UUID generation algorithm has changed,
	// which could break existing data that relies on these UUIDs
	t.Run("backward compatibility", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			namespace string
			value     string
			expected  string // Expected UUID as string
			comment   string // Description of the test case
		}{
			{
				namespace: "https://auth.test.pyck.cloud",
				value:     "123456789012345678",
				expected:  "91811523-7f2e-504e-bef0-10a58bf4512b",
				comment:   "Test org ID 1 - commonly used in tests",
			},
			{
				namespace: "https://auth.test.pyck.cloud",
				value:     "216476736127281002",
				expected:  "e4a64af0-6368-573b-9251-ae7b56e5250b",
				comment:   "Test user ID - Zitadel user ID format",
			},
			{
				namespace: "https://auth.test.pyck.cloud",
				value:     "987654321098765432",
				expected:  "864f7548-be21-57a5-a97f-b93d57c0f3e9",
				comment:   "Test org ID 2 - secondary org",
			},
			{
				namespace: "https://auth.prod.pyck.cloud",
				value:     "123456789012345678",
				expected:  "8bbe207e-93c3-5d69-b075-32f8d9a9073e",
				comment:   "Same org ID with different namespace",
			},
			{
				namespace: "https://auth.test.pyck.cloud",
				value:     "user@example.com",
				expected:  "4ebb25c7-ec20-5d3f-b1a3-44c042a99b0b",
				comment:   "Email as value - common user identifier",
			},
			{
				namespace: "system",
				value:     "admin",
				expected:  "3f8a365d-ffc1-52a9-bbc7-bde095cace05",
				comment:   "Simple namespace and value",
			},
			{
				namespace: "",
				value:     "test",
				expected:  "ba01470d-78f8-5afa-bf4c-6ef1bb6cad7f",
				comment:   "Empty namespace with value",
			},
			{
				namespace: "https://auth.test.pyck.cloud",
				value:     "",
				expected:  "f566fa4d-e68b-58c5-8637-b496e00d3a32",
				comment:   "Empty value with namespace",
			},
			{
				namespace: "",
				value:     "",
				expected:  "0e1ab8dd-36e9-53ca-a20a-df454630b80a",
				comment:   "Both namespace and value empty",
			},
			{
				namespace: "https://auth.test.pyck.cloud",
				value:     "00000000-0000-0000-0000-000000000000",
				expected:  "42147b82-e057-5bef-97ec-fc7c863131ff",
				comment:   "Nil UUID as value",
			},
		}

		for _, tc := range testCases {
			result := authn.ComputeUUID(tc.namespace, tc.value)
			expected, err := uuid.Parse(tc.expected)
			require.NoError(t, err, "Failed to parse expected UUID for case: %s", tc.comment)

			assert.Equal(t, expected, result,
				"UUID mismatch for case: %s\nNamespace: %q, Value: %q\nExpected: %s\nGot: %s",
				tc.comment, tc.namespace, tc.value, tc.expected, result.String())
		}
	})

	// Test UUID properties
	t.Run("uuid properties", func(t *testing.T) {
		t.Parallel()

		result := authn.ComputeUUID("test-namespace", "test-value")

		// Should be a valid UUID
		assert.NotEqual(t, uuid.Nil, result, "UUID should not be nil")

		// Version should be 5 (SHA-1 based)
		assert.Equal(t, uuid.Version(5), result.Version(), "UUID should be version 5 (SHA-1)")

		// Variant should be RFC4122
		assert.Equal(t, uuid.RFC4122, result.Variant(), "UUID should be RFC4122 variant")
	})

	// Test with special characters and unicode
	t.Run("special characters", func(t *testing.T) {
		t.Parallel()

		specialCases := []struct {
			namespace string
			value     string
		}{
			{"https://auth.test.pyck.cloud", "user@example.com"},
			{"namespace-with-dash", "value_with_underscore"},
			{"namespace/with/slash", "value\\with\\backslash"},
			{"namespace with spaces", "value	with	tabs"},
			{"namespace🚀with🌟emoji", "value😀with😎emoji"},
			{"namespace\nwith\nnewlines", "value\rwith\rcarriage\rreturn"},
		}

		for _, sc := range specialCases {
			uuid1 := authn.ComputeUUID(sc.namespace, sc.value)
			uuid2 := authn.ComputeUUID(sc.namespace, sc.value)

			assert.NotEqual(t, uuid.Nil, uuid1, "UUID should not be nil for special case")
			assert.Equal(t, uuid1, uuid2, "UUID should be deterministic for special characters")
		}
	})
}
