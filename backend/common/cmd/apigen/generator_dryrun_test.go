package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func minimalTestSchema(t *testing.T) *ast.Schema {
	t.Helper()

	schema, err := gqlparser.LoadSchema(&ast.Source{
		Name: "schema.graphql",
		Input: `
type Query {
	widget: String!
}
`,
	})
	if err != nil {
		t.Fatalf("failed to load test schema: %v", err)
	}
	return schema
}

func TestGenerateClientQueriesDryRun(t *testing.T) {
	t.Parallel()

	t.Run("dry-run performs no filesystem mutations", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		outputDir := filepath.Join(base, "api", "graph")
		opsDir := filepath.Join(base, "api", "operations")

		if err := generateClientQueries(minimalTestSchema(t), outputDir, opsDir, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
			t.Fatalf("expected output directory not created in dry-run, stat err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(outputDir, "apigen_gen.graphql")); !os.IsNotExist(err) {
			t.Fatalf("expected apigen_gen.graphql not written in dry-run, stat err = %v", err)
		}
		if _, err := os.Stat(handWrittenTarget(base)); !os.IsNotExist(err) {
			t.Fatalf("expected operations_gen.graphql not written in dry-run, stat err = %v", err)
		}
	})

	t.Run("non-dry-run writes the generated file", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		outputDir := filepath.Join(base, "api", "graph")
		opsDir := filepath.Join(base, "api", "operations")

		if err := generateClientQueries(minimalTestSchema(t), outputDir, opsDir, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(filepath.Join(outputDir, "apigen_gen.graphql")); err != nil {
			t.Fatalf("expected apigen_gen.graphql written, stat err = %v", err)
		}
	})
}
