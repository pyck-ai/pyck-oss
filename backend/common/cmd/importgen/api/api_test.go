package api_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	importgenapi "github.com/pyck-ai/pyck/backend/common/cmd/importgen/api"
	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/types"
)

func writeTestSchema(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "directives.graphql"),
		[]byte(`directive @pyckImportable(identityField: String!) on OBJECT`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "test.graphql")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseImportableEntities(t *testing.T) {
	t.Parallel()

	dir := writeTestSchema(t, t.TempDir(), `
		type Query {
			locations: [Location!]!
			devices: [Device!]!
		}
		type Location @pyckImportable(identityField: "name") {
			id: ID!
			name: String!
		}
		type Device @pyckImportable(identityField: "name") {
			id: ID!
			name: String!
		}
	`)

	entries, err := importgenapi.ParseImportableEntities(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if entries[0].TypeName != "Device" || !slices.Equal(entries[0].IdentityFields, []string{"name"}) {
		t.Errorf("entries[0] = %+v, want Device/name", entries[0])
	}
	if entries[1].TypeName != "Location" || !slices.Equal(entries[1].IdentityFields, []string{"name"}) {
		t.Errorf("entries[1] = %+v, want Location/name", entries[1])
	}
}

func TestParseImportableEntitiesDifferentIdentityFields(t *testing.T) {
	t.Parallel()

	dir := writeTestSchema(t, t.TempDir(), `
		type Query {
			items: [Item!]!
			repos: [Repository!]!
		}
		type Item @pyckImportable(identityField: "sku") {
			id: ID!
			sku: String!
		}
		type Repository @pyckImportable(identityField: "name") {
			id: ID!
			name: String!
		}
	`)

	entries, err := importgenapi.ParseImportableEntities(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if entries[0].TypeName != "Item" || !slices.Equal(entries[0].IdentityFields, []string{"sku"}) {
		t.Errorf("entries[0] = %+v, want Item/sku", entries[0])
	}
	if entries[1].TypeName != "Repository" || !slices.Equal(entries[1].IdentityFields, []string{"name"}) {
		t.Errorf("entries[1] = %+v, want Repository/name", entries[1])
	}
}

func TestParseImportableEntitiesCompositeIdentityAndReferences(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Directive definition must declare the args used below, or gqlparser
	// rejects them. This mirrors what entc emits in ent.graphql.
	if err := os.WriteFile(filepath.Join(dir, "directives.graphql"),
		[]byte(`directive @pyckImportable(identityField: String!, references: [String!]) on OBJECT`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test.graphql"), []byte(`
		type Query { repos: [Repository!]! }
		type Repository @pyckImportable(
			identityField: "slug,version"
			references: ["dataTypeID:DataType", "locationID:Location", "parentID:Repository"]
		) {
			id: ID!
			slug: String!
			version: Int!
		}
	`), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := importgenapi.ParseImportableEntities(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	if got := entries[0].IdentityFields; !slices.Equal(got, []string{"slug", "version"}) {
		t.Errorf("IdentityFields = %v, want [slug version]", got)
	}
	wantRefs := []types.Reference{
		{Field: "dataTypeID", TargetType: "DataType"},
		{Field: "locationID", TargetType: "Location"},
		{Field: "parentID", TargetType: "Repository"},
	}
	if got := entries[0].References; !slices.Equal(got, wantRefs) {
		t.Errorf("References = %v, want %v", got, wantRefs)
	}
}

func TestParseImportableEntitiesFailsOnMalformedReference(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "directives.graphql"),
		[]byte(`directive @pyckImportable(identityField: String!, references: [String!]) on OBJECT`), 0o600); err != nil {
		t.Fatal(err)
	}
	// "locationID Location" is missing the colon — a typo that must fail at codegen.
	if err := os.WriteFile(filepath.Join(dir, "test.graphql"), []byte(`
		type Query { repos: [Repository!]! }
		type Repository @pyckImportable(identityField: "name", references: ["locationID Location"]) {
			id: ID!
			name: String!
		}
	`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := importgenapi.ParseImportableEntities(dir)
	if err == nil {
		t.Fatal("expected error for malformed reference, got nil")
	}
	if !errors.Is(err, types.ErrMalformedReference) {
		t.Errorf("error = %v, want ErrMalformedReference", err)
	}
}

func TestParseImportableEntitiesFailsOnMalformedIdentity(t *testing.T) {
	t.Parallel()

	dir := writeTestSchema(t, t.TempDir(), `
		type Query { repos: [Repository!]! }
		type Repository @pyckImportable(identityField: "slug,") {
			id: ID!
			slug: String!
		}
	`)

	_, err := importgenapi.ParseImportableEntities(dir)
	if err == nil {
		t.Fatal("expected error for trailing-comma identityField, got nil")
	}
	if !errors.Is(err, types.ErrMalformedIdentityField) {
		t.Errorf("error = %v, want ErrMalformedIdentityField", err)
	}
}

func TestParseImportableEntitiesNoImportable(t *testing.T) {
	t.Parallel()

	dir := writeTestSchema(t, t.TempDir(), `
		type Query {
			things: [Thing!]!
		}
		type Thing {
			id: ID!
			name: String!
		}
	`)

	entries, err := importgenapi.ParseImportableEntities(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Fatalf("got %d entries, want 0", len(entries))
	}
}

func TestParseImportableEntitiesIgnoresNonObjects(t *testing.T) {
	t.Parallel()

	dir := writeTestSchema(t, t.TempDir(), `
		type Query {
			locations: [Location!]!
		}
		type Location @pyckImportable(identityField: "name") {
			id: ID!
			name: String!
		}
	`)

	entries, err := importgenapi.ParseImportableEntities(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].TypeName != "Location" {
		t.Errorf("got %q, want Location", entries[0].TypeName)
	}
}
