package importexport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		TypeName:       "Item",
		Service:        "test",
		IdentityFields: []string{"sku"},
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

// listAll returns a List func that yields the given nodes in a single page,
// ignoring pagination and the where filter (sufficient for export tests).
func listAll(nodes []map[string]any) func(context.Context, *string, *int, map[string]any) (importexport.ListResult, error) {
	return func(context.Context, *string, *int, map[string]any) (importexport.ListResult, error) {
		return importexport.ListResult{Nodes: nodes}, nil
	}
}

// TestExporterRewritesDataTypeReference verifies the DataType reference rewrite:
// DataType rows drop their server-generated id and keep (slug, version), and a
// DataMixin entity's raw dataTypeID becomes a (slug, version) $ref — even though
// "Customer" sorts before "DataType" alphabetically, proving DataType is forced
// to export first so its id→ref index is built before the rewrite.
func TestExporterRewritesDataTypeReference(t *testing.T) {
	t.Parallel()

	// This fixture omits Update; production registers one (renames are
	// importable). Either way the id is dropped because DataType has a
	// natural key — ids are only kept for types with no identity fields.
	dataType := &importexport.EntityDescriptor{
		TypeName:       "DataType",
		Service:        "management",
		IdentityFields: []string{"slug", "version"},
		List: listAll([]map[string]any{
			{"id": "dt-1", "slug": "widget", "version": float64(1), "name": "Widget v1"},
			{"id": "dt-2", "slug": "widget", "version": float64(2), "name": "Widget v2"},
		}),
	}
	customer := &importexport.EntityDescriptor{
		TypeName:       "Customer",
		Service:        "main-data",
		IdentityFields: nil, // create-only, no natural key
		References:     []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}},
		List: listAll([]map[string]any{
			{"id": "c-1", "dataTypeID": "dt-2", "dataTypeSlug": "widget", "data": map[string]any{"name": "Acme"}},
		}),
	}

	reg := importexport.NewRegistry()
	if err := reg.Register(dataType); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(customer); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.ExportToDir(context.Background(), dir, nil); err != nil {
		t.Fatalf("export: %v", err)
	}

	// DataType rows: no id, version preserved.
	for _, rec := range readExportedJSONL(t, filepath.Join(dir, "datatype.jsonl")) {
		if _, hasID := rec["id"]; hasID {
			t.Errorf("DataType export still carries id: %v", rec["id"])
		}
		if rec["version"] == nil {
			t.Errorf("DataType export missing version: %v", rec)
		}
	}

	// Customer's dataTypeID is rewritten to a (slug, version) $ref pointing at
	// the exact version (dt-2 → version 2); the raw uuid and dataTypeSlug are gone.
	recs := readExportedJSONL(t, filepath.Join(dir, "customer.jsonl"))
	if len(recs) != 1 {
		t.Fatalf("got %d customer records, want 1", len(recs))
	}
	cust := recs[0]
	if _, ok := cust["dataTypeSlug"]; ok {
		t.Error("customer export should drop server-derived dataTypeSlug")
	}
	ref, ok := cust["dataTypeID"].(map[string]any)
	if !ok {
		t.Fatalf("customer dataTypeID is not a $ref: %v", cust["dataTypeID"])
	}
	target, ok := ref["$ref"].(map[string]any)
	if !ok {
		t.Fatalf("customer dataTypeID missing $ref object: %v", ref)
	}
	if target["__typename"] != "DataType" || target["slug"] != "widget" || target["version"] != float64(2) {
		t.Errorf("customer $ref = %v, want DataType/widget/version 2", target)
	}
}

// TestExporterDependencyOrder proves the recursive emitter orders entities by
// declared references (target before referrer) across a multi-hop chain
// registered out of order, and orders self-referencing rows parents-first.
func TestExporterDependencyOrder(t *testing.T) {
	t.Parallel()

	// C -> B -> A chain (each keyed by "name"), plus a self-referencing Tree
	// whose child points at its parent. Registered/listed in NON-dependency order.
	a := &importexport.EntityDescriptor{
		TypeName: "A", Service: "t", IdentityFields: []string{"name"},
		List: listAll([]map[string]any{{"id": "a1", "name": "A1"}}),
	}
	b := &importexport.EntityDescriptor{
		TypeName: "B", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "aID", TargetType: "A"}},
		List:       listAll([]map[string]any{{"id": "b1", "name": "B1", "aID": "a1"}}),
	}
	c := &importexport.EntityDescriptor{
		TypeName: "C", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "bID", TargetType: "B"}},
		List:       listAll([]map[string]any{{"id": "c1", "name": "C1", "bID": "b1"}}),
	}
	// Tree rows listed child-first to prove row-level reordering.
	tree := &importexport.EntityDescriptor{
		TypeName: "Tree", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "parentID", TargetType: "Tree"}},
		List: listAll([]map[string]any{
			{"id": "t3", "name": "leaf", "parentID": "t2"},
			{"id": "t2", "name": "mid", "parentID": "t1"},
			{"id": "t1", "name": "root"},
		}),
	}

	reg := importexport.NewRegistry()
	for _, d := range []*importexport.EntityDescriptor{c, a, tree, b} { // registration order is irrelevant
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &out, nil); err != nil {
		t.Fatalf("export: %v", err)
	}

	records := parseStreamJSONL(t, out.String())
	order := make([]string, 0, len(records))
	for _, rec := range records {
		order = append(order, rec["__typename"].(string)+":"+rec["name"].(string))
	}

	// Assert each referrer appears after its target.
	mustPrecede := [][2]string{
		{"A:A1", "B:B1"},
		{"B:B1", "C:C1"},
		{"Tree:root", "Tree:mid"},
		{"Tree:mid", "Tree:leaf"},
	}
	pos := map[string]int{}
	for i, k := range order {
		pos[k] = i
	}
	for _, pair := range mustPrecede {
		if pos[pair[0]] >= pos[pair[1]] {
			t.Errorf("expected %s before %s; order = %v", pair[0], pair[1], order)
		}
	}
}

// parseStreamJSONL parses a JSONL byte stream into one map per non-empty line.
func parseStreamJSONL(t *testing.T, s string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

// readExportedJSONL parses an exported JSONL file into one map per line.
func readExportedJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return parseStreamJSONL(t, string(data))
}

// TestExporterVersionOrderWithEarlySortingReferrer reproduces the re-import
// failure behind the (slug, version) portability contract: the emitter's
// alphabetical seed order let a referrer type sorting before "DataType"
// (here "Customer") pull the version it pins ahead of lower versions of the
// same slug family. The import side (createDataType's append-only reconcile)
// rejects a version below the family's current MAX, so a datatype file with
// descending versions cannot be re-imported. Rows of one type must be
// emitted so that same-slug DataType versions ascend, regardless of which
// referrer seeds the DFS first.
func TestExporterVersionOrderWithEarlySortingReferrer(t *testing.T) {
	t.Parallel()

	// Two versions of one slug family. IDs deliberately ordered so that the
	// raw-id sort ("01" < "02") matches version order — the failure comes
	// from the referrer hoisting v2, not from id ordering.
	dataType := &importexport.EntityDescriptor{
		TypeName: "DataType", Service: "t", IdentityFields: []string{"slug", "version"},
		List: listAll([]map[string]any{
			{"id": "01", "slug": "widget", "version": 1},
			{"id": "02", "slug": "widget", "version": 2},
		}),
	}
	// "Customer" sorts alphabetically before "DataType" and pins the HIGHER
	// version, so a seed-order DFS emits DataType v2 before v1.
	customer := &importexport.EntityDescriptor{
		TypeName: "Customer", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}},
		List:       listAll([]map[string]any{{"id": "c1", "name": "C1", "dataTypeID": "02"}}),
	}

	reg := importexport.NewRegistry()
	for _, d := range []*importexport.EntityDescriptor{customer, dataType} {
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &out, nil); err != nil {
		t.Fatalf("export: %v", err)
	}

	var versions []float64
	customerPos, lastDataTypePos := -1, -1
	for i, rec := range parseStreamJSONL(t, out.String()) {
		switch rec["__typename"] {
		case "DataType":
			v, ok := rec["version"].(float64)
			if !ok {
				t.Fatalf("record %d: version is %T, want float64", i, rec["version"])
			}
			versions = append(versions, v)
			lastDataTypePos = i
		case "Customer":
			customerPos = i
		}
	}

	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Errorf("DataType versions emitted as %v, want ascending [1 2]", versions)
	}
	// The dependency contract must survive the reordering: the pinned
	// DataType still precedes its referrer.
	if customerPos < lastDataTypePos {
		t.Errorf("Customer emitted at %d before last DataType at %d", customerPos, lastDataTypePos)
	}
}

// A row pinned to a target the export cannot see (a soft-deleted DataType
// version is hidden from List) must fail the export loudly. Dropping the
// reference produces a file whose rows import either unpinned or with an
// error naming neither the row nor the version that caused it — silent data
// loss discovered long after the export.
func TestExporterUnresolvableReferenceFailsLoudly(t *testing.T) {
	t.Parallel()

	// The DataType list omits "deleted-v1", which the customer still pins.
	dataType := &importexport.EntityDescriptor{
		TypeName: "DataType", Service: "t", IdentityFields: []string{"slug", "version"},
		List: listAll([]map[string]any{{"id": "live", "slug": "widget", "version": 2}}),
	}
	customer := &importexport.EntityDescriptor{
		TypeName: "Customer", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}},
		List:       listAll([]map[string]any{{"id": "c1", "name": "C1", "dataTypeID": "deleted-v1"}}),
	}

	reg := importexport.NewRegistry()
	for _, d := range []*importexport.EntityDescriptor{customer, dataType} {
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	err := exp.Export(context.Background(), &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("export succeeded despite an unresolvable reference")
	}
	if !errors.Is(err, importexport.ErrUnresolvedReference) {
		t.Errorf("error = %v, want ErrUnresolvedReference", err)
	}
	for _, want := range []string{"Customer", "dataTypeID", "DataType", "deleted-v1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// A deliberate subset export (the target type was not requested) keeps its
// existing behaviour: the reference is omitted, not an error — the operator
// asked for one type and the raw FK id is not portable.
func TestExporterSubsetExportOmitsOutOfSetReference(t *testing.T) {
	t.Parallel()

	dataType := &importexport.EntityDescriptor{
		TypeName: "DataType", Service: "t", IdentityFields: []string{"slug", "version"},
		List: listAll([]map[string]any{{"id": "dt1", "slug": "widget", "version": 1}}),
	}
	customer := &importexport.EntityDescriptor{
		TypeName: "Customer", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}},
		List:       listAll([]map[string]any{{"id": "c1", "name": "C1", "dataTypeID": "dt1"}}),
	}

	reg := importexport.NewRegistry()
	for _, d := range []*importexport.EntityDescriptor{customer, dataType} {
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	if err := exp.Export(context.Background(), &out, []string{"Customer"}); err != nil {
		t.Fatalf("subset export: %v", err)
	}

	recs := parseStreamJSONL(t, out.String())
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if _, ok := recs[0]["dataTypeID"]; ok {
		t.Errorf("out-of-set reference should be omitted: %v", recs[0])
	}
}

// N dangling pins must surface in ONE run: the export aborts either way,
// but reporting only the first unresolved reference costs the operator one
// full export/fix cycle per bad row.
func TestExporterAggregatesAllUnresolvedReferences(t *testing.T) {
	t.Parallel()

	dataType := &importexport.EntityDescriptor{
		TypeName: "DataType", Service: "t", IdentityFields: []string{"slug", "version"},
		List: listAll([]map[string]any{{"id": "live", "slug": "widget", "version": 1}}),
	}
	customer := &importexport.EntityDescriptor{
		TypeName: "Customer", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}},
		List: listAll([]map[string]any{
			{"id": "c1", "name": "C1", "dataTypeID": "gone-1"},
			{"id": "c2", "name": "C2", "dataTypeID": "gone-2"},
			{"id": "c3", "name": "C3", "dataTypeID": "live"},
		}),
	}
	location := &importexport.EntityDescriptor{
		TypeName: "Location", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}},
		List:       listAll([]map[string]any{{"id": "l1", "name": "L1", "dataTypeID": "gone-3"}}),
	}

	reg := importexport.NewRegistry()
	for _, d := range []*importexport.EntityDescriptor{customer, dataType, location} {
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
	err := exp.Export(context.Background(), &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("export succeeded despite unresolved references")
	}
	if !errors.Is(err, importexport.ErrUnresolvedReference) {
		t.Fatalf("error = %v, want ErrUnresolvedReference", err)
	}
	for _, want := range []string{"C1", "gone-1", "C2", "gone-2", "L1", "gone-3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("one run must report every dangling pin; error %q does not name %q", err, want)
		}
	}
}

// A recovery export must be able to finish: with the skip option the
// dangling references are omitted with a warning per row instead of
// failing, while resolvable references still become $refs.
func TestExporterSkipUnresolvedReferences(t *testing.T) {
	t.Parallel()

	dataType := &importexport.EntityDescriptor{
		TypeName: "DataType", Service: "t", IdentityFields: []string{"slug", "version"},
		List: listAll([]map[string]any{{"id": "live", "slug": "widget", "version": 1}}),
	}
	customer := &importexport.EntityDescriptor{
		TypeName: "Customer", Service: "t", IdentityFields: []string{"name"},
		References: []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}},
		List: listAll([]map[string]any{
			{"id": "c1", "name": "Dangling", "dataTypeID": "gone-1"},
			{"id": "c2", "name": "Pinned", "dataTypeID": "live"},
		}),
	}

	reg := importexport.NewRegistry()
	for _, d := range []*importexport.EntityDescriptor{customer, dataType} {
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	var progress bytes.Buffer
	var out bytes.Buffer
	exp := importexport.NewExporter(reg,
		importexport.WithExportOutput(&progress),
		importexport.WithSkipUnresolvedReferences(true))
	if err := exp.Export(context.Background(), &out, nil); err != nil {
		t.Fatalf("skip mode must complete: %v", err)
	}

	byName := map[string]map[string]any{}
	for _, rec := range parseStreamJSONL(t, out.String()) {
		if rec["__typename"] == "Customer" {
			byName[rec["name"].(string)] = rec
		}
	}
	if _, ok := byName["Dangling"]["dataTypeID"]; ok {
		t.Errorf("dangling reference must be omitted in skip mode: %v", byName["Dangling"])
	}
	if _, ok := byName["Pinned"]["dataTypeID"].(map[string]any); !ok {
		t.Errorf("resolvable reference must still be rewritten to a $ref: %v", byName["Pinned"])
	}
	for _, want := range []string{"Dangling", "gone-1"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("skip mode must warn per row; progress output %q does not name %q", progress.String(), want)
		}
	}
}
