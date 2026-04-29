package importexport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

func TestExporterBasic(t *testing.T) {
	t.Parallel()

	desc := fakeDescriptorWithData("Location", []map[string]any{
		{"id": "1", "name": "A", "data": "x"},
		{"id": "2", "name": "B", "data": "y"},
	})
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &out, nil); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["__typename"] != "Location" {
		t.Errorf("__typename = %v, want Location", record["__typename"])
	}
	if record["name"] != "A" {
		t.Errorf("name = %v, want A", record["name"])
	}
}

func TestExporterStripsServerManagedFields(t *testing.T) {
	t.Parallel()

	desc := fakeDescriptorWithData("Location", []map[string]any{
		{
			"id":        "1",
			"name":      "A",
			"tenantID":  "tenant-1",
			"createdAt": "2026-01-01",
			"createdBy": "user-1",
			"updatedAt": "2026-01-02",
			"updatedBy": "user-2",
			"deletedAt": nil,
			"deletedBy": nil,
			"data":      "keep-me",
		},
	})
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &out, nil); err != nil {
		t.Fatal(err)
	}

	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}

	// id should be stripped by default.
	if _, ok := record["id"]; ok {
		t.Error("id should be stripped by default")
	}
	// Server-managed fields should be stripped.
	for _, field := range []string{"tenantID", "createdAt", "createdBy", "updatedAt", "updatedBy", "deletedAt", "deletedBy"} {
		if _, ok := record[field]; ok {
			t.Errorf("%s should be stripped", field)
		}
	}
	// Business fields should be kept.
	if record["name"] != "A" {
		t.Error("name should be preserved")
	}
	if record["data"] != "keep-me" {
		t.Error("data should be preserved")
	}
	if record["__typename"] != "Location" {
		t.Error("__typename should be added")
	}
}

func TestExporterFilterByType(t *testing.T) {
	t.Parallel()

	locDesc := fakeDescriptorWithData("Location", []map[string]any{
		{"id": "1", "name": "Loc-A"},
	})
	devDesc := fakeDescriptorWithData("Device", []map[string]any{
		{"id": "2", "name": "Dev-A"},
	})
	reg := importexport.NewRegistry()
	if err := reg.Register(locDesc); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(devDesc); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))

	// Export only Location.
	if err := exp.Export(context.Background(), &out, []string{"Location"}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["__typename"] != "Location" {
		t.Errorf("__typename = %v, want Location", record["__typename"])
	}
}

func TestExporterEmptyRegistry(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))

	err := exp.Export(context.Background(), &out, nil)
	if err == nil {
		t.Fatal("expected error for empty registry")
	}
	if !strings.Contains(err.Error(), "no entity types") {
		t.Errorf("error = %q, want 'no entity types'", err)
	}
}

func TestExporterUnknownTypeFilter(t *testing.T) {
	t.Parallel()

	desc := fakeDescriptorWithData("Location", []map[string]any{
		{"id": "1", "name": "A"},
	})
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))

	err := exp.Export(context.Background(), &out, []string{"NonExistent"})
	if err == nil {
		t.Fatal("expected error for unknown type filter")
	}
}

func TestExporterUnicodeValues(t *testing.T) {
	t.Parallel()

	desc := fakeDescriptorWithData("Location", []map[string]any{
		{"id": "1", "name": "🏭 Factory", "data": map[string]any{"emoji": "👨\u200d👩\u200d👧\u200d👦", "japanese": "日本語"}}, //nolint:gosmopolitan // intentionally testing unicode handling
	})
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &out, nil); err != nil {
		t.Fatal(err)
	}

	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["name"] != "🏭 Factory" {
		t.Errorf("name = %v, want emoji name", record["name"])
	}
	data := record["data"].(map[string]any)
	if data["japanese"] != "日本語" { //nolint:gosmopolitan // intentionally testing unicode handling
		t.Errorf("japanese = %v, want 日本語", data["japanese"]) //nolint:gosmopolitan // intentionally testing unicode handling
	}
}

func TestExporterPagination(t *testing.T) {
	t.Parallel()

	// Create a descriptor that returns 2 pages of 2 entities each.
	callCount := 0
	desc := &importexport.EntityDescriptor{
		TypeName:      "Item",
		Service:       "test",
		IdentityField: "sku",
		List: func(_ context.Context, after *string, _ *int, _ map[string]any) (importexport.ListResult, error) {
			callCount++
			if after == nil {
				cursor := "cursor-1"
				return importexport.ListResult{
					Nodes:       []map[string]any{{"id": "1", "sku": "A"}, {"id": "2", "sku": "B"}},
					HasNextPage: true,
					EndCursor:   &cursor,
				}, nil
			}
			return importexport.ListResult{
				Nodes:       []map[string]any{{"id": "3", "sku": "C"}, {"id": "4", "sku": "D"}},
				HasNextPage: false,
			}, nil
		},
	}
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &out, nil); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4 (2 pages x 2 entities)", len(lines))
	}
	if callCount != 2 {
		t.Errorf("List called %d times, want 2 (one per page)", callCount)
	}
}

func TestExporterRoundTrip(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	// Import some entities first.
	var importBuf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&importBuf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "input.jsonl",
		`{"__typename": "Location", "name": "Alpha", "zone": "A", "floor": 1}
{"__typename": "Location", "name": "Beta", "zone": "B", "floor": 2}`)

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(*store) != 2 {
		t.Fatalf("store has %d entities, want 2", len(*store))
	}

	// Export.
	var exportBuf bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &exportBuf, nil); err != nil {
		t.Fatal(err)
	}

	// Parse exported lines and verify content.
	lines := strings.Split(strings.TrimSpace(exportBuf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("exported %d lines, want 2", len(lines))
	}

	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["__typename"] != "Location" {
			t.Errorf("__typename = %v, want Location", record["__typename"])
		}
		name, ok := record["name"].(string)
		if !ok {
			t.Fatal("name missing")
		}
		if name != "Alpha" && name != "Beta" {
			t.Errorf("unexpected name: %s", name)
		}
		// id should be stripped.
		if _, ok := record["id"]; ok {
			t.Error("id should be stripped in export")
		}
	}

	// Re-import exported data into a fresh store — should create, not error.
	desc2, store2 := fakeDescriptor("Location")
	reg2 := importexport.NewRegistry()
	if err := reg2.Register(desc2); err != nil {
		t.Fatal(err)
	}

	exportPath := writeJSONL(t, dir, "exported.jsonl", exportBuf.String())
	imp2 := importexport.NewImporter(reg2, importexport.WithOutput(&bytes.Buffer{}))
	result, err := imp2.ImportFiles(context.Background(), []string{exportPath})
	if err != nil {
		t.Fatalf("re-import failed: %v", err)
	}
	if result.Created != 2 {
		t.Errorf("re-import Created = %d, want 2", result.Created)
	}
	if len(*store2) != 2 {
		t.Errorf("re-import store has %d entities, want 2", len(*store2))
	}
}

func TestExporterToDir(t *testing.T) {
	t.Parallel()

	locDesc := fakeDescriptorWithData("Location", []map[string]any{
		{"id": "1", "name": "A"},
		{"id": "2", "name": "B"},
	})
	devDesc := fakeDescriptorWithData("Device", []map[string]any{
		{"id": "3", "name": "Scanner-1"},
	})
	reg := importexport.NewRegistry()
	if err := reg.Register(locDesc); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(devDesc); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.ExportToDir(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}

	// Check location.jsonl exists with 2 lines.
	locData, err := os.ReadFile(filepath.Join(dir, "location.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	locLines := strings.Split(strings.TrimSpace(string(locData)), "\n")
	if len(locLines) != 2 {
		t.Fatalf("location.jsonl has %d lines, want 2", len(locLines))
	}

	// Check device.jsonl exists with 1 line.
	devData, err := os.ReadFile(filepath.Join(dir, "device.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	devLines := strings.Split(strings.TrimSpace(string(devData)), "\n")
	if len(devLines) != 1 {
		t.Fatalf("device.jsonl has %d lines, want 1", len(devLines))
	}

	// Verify __typename in location file.
	var record map[string]any
	if err := json.Unmarshal([]byte(locLines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["__typename"] != "Location" {
		t.Errorf("__typename = %v, want Location", record["__typename"])
	}
}

func TestExporterToDirWithTypeFilter(t *testing.T) {
	t.Parallel()

	locDesc := fakeDescriptorWithData("Location", []map[string]any{
		{"id": "1", "name": "A"},
	})
	devDesc := fakeDescriptorWithData("Device", []map[string]any{
		{"id": "2", "name": "Scanner-1"},
	})
	reg := importexport.NewRegistry()
	if err := reg.Register(locDesc); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(devDesc); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))

	// Export only Location.
	if err := exp.ExportToDir(context.Background(), dir, []string{"Location"}); err != nil {
		t.Fatal(err)
	}

	// location.jsonl should exist.
	if _, err := os.Stat(filepath.Join(dir, "location.jsonl")); err != nil {
		t.Errorf("location.jsonl should exist: %v", err)
	}
	// device.jsonl should NOT exist.
	if _, err := os.Stat(filepath.Join(dir, "device.jsonl")); !os.IsNotExist(err) {
		t.Errorf("device.jsonl should not exist")
	}
}

func TestExporterToDirRoundTrip(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	// Import entities.
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))
	dir := t.TempDir()
	path := writeJSONL(t, dir, "input.jsonl",
		`{"__typename": "Location", "name": "Alpha", "zone": "A"}
{"__typename": "Location", "name": "Beta", "zone": "B"}`)
	if _, err := imp.ImportFiles(context.Background(), []string{path}); err != nil {
		t.Fatal(err)
	}

	// Export to directory.
	exportDir := filepath.Join(dir, "export")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.ExportToDir(context.Background(), exportDir, nil); err != nil {
		t.Fatal(err)
	}

	// Re-import from directory into fresh store.
	desc2, store2 := fakeDescriptor("Location")
	reg2 := importexport.NewRegistry()
	if err := reg2.Register(desc2); err != nil {
		t.Fatal(err)
	}
	imp2 := importexport.NewImporter(reg2, importexport.WithOutput(&bytes.Buffer{}))

	result, err := imp2.ImportFiles(context.Background(), []string{exportDir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 {
		t.Errorf("Created = %d, want 2", result.Created)
	}
	if len(*store) != len(*store2) {
		t.Errorf("store sizes differ: original=%d, reimported=%d", len(*store), len(*store2))
	}
}
