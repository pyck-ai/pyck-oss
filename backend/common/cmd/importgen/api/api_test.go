package api_test

import (
	"os"
	"path/filepath"
	"testing"

	importgenapi "github.com/pyck-ai/pyck/backend/common/cmd/importgen/api"
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

	if entries[0].TypeName != "Device" || entries[0].IdentityField != "name" {
		t.Errorf("entries[0] = %+v, want Device/name", entries[0])
	}
	if entries[1].TypeName != "Location" || entries[1].IdentityField != "name" {
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

	if entries[0].TypeName != "Item" || entries[0].IdentityField != "sku" {
		t.Errorf("entries[0] = %+v, want Item/sku", entries[0])
	}
	if entries[1].TypeName != "Repository" || entries[1].IdentityField != "name" {
		t.Errorf("entries[1] = %+v, want Repository/name", entries[1])
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
