package main

import (
	"testing"
)

func TestHelperFunctions(t *testing.T) {
	t.Parallel()

	t.Run("capitalize", func(t *testing.T) {
		t.Parallel()

		tests := []struct{ in, want string }{
			{"hello", "Hello"},
			{"Hello", "Hello"},
			{"", ""},
			{"a", "A"},
		}
		for _, tt := range tests {
			if got := capitalize(tt.in); got != tt.want {
				t.Errorf("capitalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	})

	t.Run("normalize", func(t *testing.T) {
		t.Parallel()

		tests := []struct{ in, want string }{
			{"name", "GetName"},
			{"getName", "GetName"},
			{"GetName", "GetName"},
			{"", ""},
			{"id", "GetId"},
		}
		for _, tt := range tests {
			if got := normalize(tt.in); got != tt.want {
				t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	})

	t.Run("isIntrospectionField", func(t *testing.T) {
		t.Parallel()

		if !isIntrospectionField("__typename") {
			t.Error("expected __typename to be introspection field")
		}
		if isIntrospectionField("name") {
			t.Error("expected name to NOT be introspection field")
		}
	})

	t.Run("isConnectionType", func(t *testing.T) {
		t.Parallel()

		if !isConnectionType("RepositoryConnection") {
			t.Error("expected RepositoryConnection to be connection type")
		}
		if isConnectionType("Repository") {
			t.Error("expected Repository to NOT be connection type")
		}
	})
}
