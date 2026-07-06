package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHandWrittenOperations writes to filepath.Dir(outputDir). The tests pass an
// outputDir of <tmp>/api/graph so the generated file lands at <tmp>/api.
func handWrittenTarget(base string) string {
	return filepath.Join(base, "api", handWrittenOperationsFile)
}

func TestWriteHandWrittenOperations(t *testing.T) {
	t.Parallel()

	t.Run("missing directory is a no-op", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		outputDir := filepath.Join(base, "api", "graph")

		if err := writeHandWrittenOperations(filepath.Join(base, "does-not-exist"), outputDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Stat(handWrittenTarget(base)); !os.IsNotExist(err) {
			t.Fatalf("expected no generated file, stat err = %v", err)
		}
	})

	t.Run("valid operations are concatenated into the generated file", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		outputDir := filepath.Join(base, "api", "graph")
		opsDir := filepath.Join(base, "api", "operations")
		mustWrite(t, filepath.Join(opsDir, "b.graphql"), "query B { b }")
		mustWrite(t, filepath.Join(opsDir, "a.graphql"), "query A { a }")

		if err := writeHandWrittenOperations(opsDir, outputDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := mustRead(t, handWrittenTarget(base))
		if !strings.Contains(got, "DO NOT EDIT") {
			t.Errorf("expected generated header, got:\n%s", got)
		}
		// Sorted by filename: a before b.
		if i, j := strings.Index(got, "query A"), strings.Index(got, "query B"); i < 0 || j < 0 || i > j {
			t.Errorf("operations missing or out of order (a=%d b=%d):\n%s", i, j, got)
		}
	})

	t.Run("invalid GraphQL is rejected", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		outputDir := filepath.Join(base, "api", "graph")
		opsDir := filepath.Join(base, "api", "operations")
		mustWrite(t, filepath.Join(opsDir, "bad.graphql"), "query Bad { unterminated ")

		if err := writeHandWrittenOperations(opsDir, outputDir); err == nil {
			t.Fatal("expected a syntax error, got nil")
		}
	})

	t.Run("emptied directory removes a stale generated file", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		outputDir := filepath.Join(base, "api", "graph")
		opsDir := filepath.Join(base, "api", "operations")
		mustWrite(t, filepath.Join(opsDir, "a.graphql"), "query A { a }")
		if err := writeHandWrittenOperations(opsDir, outputDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Remove the source operations, then regenerate: the stale file must go.
		if err := os.Remove(filepath.Join(opsDir, "a.graphql")); err != nil {
			t.Fatal(err)
		}
		if err := writeHandWrittenOperations(opsDir, outputDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Stat(handWrittenTarget(base)); !os.IsNotExist(err) {
			t.Fatalf("expected stale file removed, stat err = %v", err)
		}
	})
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
