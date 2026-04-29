package internal_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/internal"
)

func writeClientGen(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "client_gen.go")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectAccessorChain_DirectEntity(t *testing.T) {
	t.Parallel()

	// Entity struct has ID field directly → 1-level accessor.
	path := writeClientGen(t, `package api
type CreateDataType_CreateDataType struct {
	CreatedAt string
	ID        string
	Name      string
}
`)

	got, err := internal.DetectAccessorChain(path, "CreateDataType")
	if err != nil {
		t.Fatal(err)
	}
	want := "GetCreateDataType()"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDetectAccessorChain_NullableWrapper(t *testing.T) {
	t.Parallel()

	// Wrapper with pointer entity field (nullable GraphQL field).
	path := writeClientGen(t, `package api
type CreateItem_CreateItem struct {
	InventoryItem *CreateItem_CreateItem_InventoryItem
	Workflows     []*CreateItem_CreateItem_Workflows
}
type CreateItem_CreateItem_InventoryItem struct {
	ID  string
	Sku string
}
type CreateItem_CreateItem_Workflows struct {
	ID string
}
`)

	got, err := internal.DetectAccessorChain(path, "CreateItem")
	if err != nil {
		t.Fatal(err)
	}
	want := "GetCreateItem().GetInventoryItem()"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDetectAccessorChain_NonNullableWrapper(t *testing.T) {
	t.Parallel()

	// Wrapper with value-type entity field (non-nullable GraphQL field).
	path := writeClientGen(t, `package api
type CreateDevice_CreateDevice struct {
	Device    CreateDevice_CreateDevice_Device
	Workflows []*CreateDevice_CreateDevice_Workflows
}
type CreateDevice_CreateDevice_Device struct {
	ID   string
	Name string
}
type CreateDevice_CreateDevice_Workflows struct {
	ID string
}
`)

	got, err := internal.DetectAccessorChain(path, "CreateDevice")
	if err != nil {
		t.Fatal(err)
	}
	want := "GetCreateDevice().GetDevice()"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDetectAccessorChain_MissingStruct(t *testing.T) {
	t.Parallel()

	// No inner struct found → fallback to 1-level.
	path := writeClientGen(t, `package api
type Unrelated struct { ID string }
`)

	got, err := internal.DetectAccessorChain(path, "CreateThing")
	if err != nil {
		t.Fatal(err)
	}
	want := "GetCreateThing()"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
