package importexport_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

// fakeCompositeDescriptor builds a create-only descriptor keyed on a composite
// identity (e.g. DataType's (slug, version)). It mirrors fakeDescriptor but with
// no Update func, so an existing match is skipped rather than updated.
func fakeCompositeDescriptor(typeName string, identity ...string) (*importexport.EntityDescriptor, *[]map[string]any) {
	store := &[]map[string]any{}
	nextID := 0
	return &importexport.EntityDescriptor{
		TypeName:       typeName,
		Service:        "test",
		IdentityFields: identity,
		List: func(_ context.Context, _ *string, _ *int, where map[string]any) (importexport.ListResult, error) {
			var nodes []map[string]any
			for _, e := range *store {
				match := true
				for k, v := range where {
					if e[k] != v {
						match = false
						break
					}
				}
				if match {
					nodes = append(nodes, e)
				}
			}
			return importexport.ListResult{Nodes: nodes}, nil
		},
		Create: func(_ context.Context, input map[string]any) (map[string]any, error) {
			nextID++
			e := make(map[string]any, len(input)+1)
			for k, v := range input {
				e[k] = v
			}
			e["id"] = fmt.Sprintf("id-%d", nextID)
			*store = append(*store, e)
			return e, nil
		},
	}, store
}

// TestImporterFoundButUnusableIDErrorsNotDuplicate verifies that when the
// existence-check List returns a matching row whose id is unusable (missing /
// non-string), the importer fails loudly instead of treating it as not-found
// and creating a duplicate of an entity that already exists.
func TestImporterFoundButUnusableIDErrorsNotDuplicate(t *testing.T) {
	t.Parallel()

	created := 0
	desc := &importexport.EntityDescriptor{
		TypeName:       "Location",
		Service:        "test",
		IdentityFields: []string{"name"},
		List: func(_ context.Context, _ *string, _ *int, _ map[string]any) (importexport.ListResult, error) {
			// Row matches, but its id is not a usable string.
			return importexport.ListResult{Nodes: []map[string]any{{"name": "A", "id": nil}}}, nil
		},
		Create: func(_ context.Context, _ map[string]any) (map[string]any, error) {
			created++
			return map[string]any{"id": "new-id"}, nil
		},
	}
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	imp := importexport.NewImporter(reg, importexport.WithOutput(&bytes.Buffer{}), importexport.WithContinueOnError(true))
	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename":"Location","name":"A"}`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatalf("ImportFiles returned a hard error: %v", err)
	}
	if created != 0 {
		t.Errorf("created %d duplicate(s) of an existing row; want a loud error instead", created)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1", len(result.Errors))
	}
	if !errors.Is(result.Errors[0].Err, importexport.ErrRefNoID) {
		t.Errorf("error = %v, want ErrRefNoID", result.Errors[0].Err)
	}
}

// TestImporterCompositeIdentitySkipsDuplicateVersion verifies that a create-only
// entity keyed on (slug, version) treats different versions of one slug as
// distinct rows, while re-importing the exact same (slug, version) is skipped —
// the import side of DataType's append-only versioning.
func TestImporterCompositeIdentitySkipsDuplicateVersion(t *testing.T) {
	t.Parallel()

	desc, store := fakeCompositeDescriptor("DataType", "slug", "version")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	imp := importexport.NewImporter(reg, importexport.WithOutput(&bytes.Buffer{}))
	dir := t.TempDir()
	path := writeJSONL(t, dir, "dt.jsonl", `{"__typename":"DataType","slug":"widget","version":1,"jsonSchema":"{}"}
{"__typename":"DataType","slug":"widget","version":1,"jsonSchema":"{}"}
{"__typename":"DataType","slug":"widget","version":2,"jsonSchema":"{}"}
`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 {
		t.Errorf("Created = %d, want 2 (v1 + v2)", result.Created)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (duplicate v1)", result.Skipped)
	}
	if len(*store) != 2 {
		t.Errorf("store has %d rows, want 2", len(*store))
	}

	// A record missing a composite-identity field has no identity → always
	// created (can't existence-check), never skipped.
	path2 := writeJSONL(t, dir, "dt2.jsonl",
		`{"__typename":"DataType","slug":"widget","jsonSchema":"{}"}`)
	result2, err := imp.ImportFiles(context.Background(), []string{path2})
	if err != nil {
		t.Fatal(err)
	}
	if result2.Created != 1 || result2.Skipped != 0 {
		t.Errorf("incomplete-identity record: created=%d skipped=%d, want created=1 skipped=0", result2.Created, result2.Skipped)
	}
}

func TestImporterCreateAndUpdate(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "Location", "name": "A", "data": "x"}
{"__typename": "Location", "name": "B", "data": "y"}
`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}

	if result.Created != 2 {
		t.Errorf("Created = %d, want 2", result.Created)
	}
	if len(*store) != 2 {
		t.Fatalf("store has %d entities, want 2", len(*store))
	}

	// Import again — should update, not create.
	path2 := writeJSONL(t, dir, "test2.jsonl", `{"__typename": "Location", "name": "A", "data": "updated"}
`)
	result2, err := imp.ImportFiles(context.Background(), []string{path2})
	if err != nil {
		t.Fatal(err)
	}
	if result2.Updated != 1 {
		t.Errorf("Updated = %d, want 1", result2.Updated)
	}
	if (*store)[0]["data"] != "updated" {
		t.Errorf("data = %v, want 'updated'", (*store)[0]["data"])
	}
}

func TestImporterUnknownTypename(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "NonExistent", "name": "A"}
`)

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for unknown typename")
	}
	if !strings.Contains(err.Error(), "unknown entity type") {
		t.Errorf("error = %q, want 'unknown entity type'", err)
	}
}

func TestImporterMissingTypename(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"name": "A"}
`)

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for missing __typename")
	}
	if !strings.Contains(err.Error(), "__typename") {
		t.Errorf("error = %q, want mention of __typename", err)
	}
}

func TestImporterEmptyTypename(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "", "name": "A"}
`)

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for empty __typename")
	}
}

func TestImporterNullTypename(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": null, "name": "A"}
`)

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for null __typename")
	}
}

func TestImporterNumericTypename(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": 42, "name": "A"}
`)

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for numeric __typename")
	}
}

func TestImporterMalformedJSON(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{not json at all}
`)

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestImporterNonUTF8(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	// Invalid UTF-8 bytes inside a JSON string value.
	content := []byte(`{"__typename": "Loc", "name": "test` + "\x80\x81" + `"}` + "\n")
	path := filepath.Join(dir, "test.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for non-UTF-8 content")
	}
}

func TestImporterBinaryContent(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	// Pure binary garbage.
	content := []byte{0x00, 0xFF, 0xFE, 0x89, 0x50, 0x4E, 0x47, '\n'}
	path := filepath.Join(dir, "test.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for binary content")
	}
}

func TestImporterContinueOnError(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf), importexport.WithContinueOnError(true))

	dir := t.TempDir()
	// Line 1: valid, Line 2: bad typename, Line 3: valid.
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "Location", "name": "A"}
{"__typename": "BadType", "name": "B"}
{"__typename": "Location", "name": "C"}
`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatalf("unexpected error with continue-on-error: %v", err)
	}
	if result.Created != 2 {
		t.Errorf("Created = %d, want 2", result.Created)
	}
	if len(result.Errors) != 1 {
		t.Errorf("Errors = %d, want 1", len(result.Errors))
	}
	if len(*store) != 2 {
		t.Errorf("store has %d entities, want 2", len(*store))
	}
}

func TestImporterDryRun(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf), importexport.WithDryRun(true))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "Location", "name": "A"}
`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Updated != 0 {
		t.Errorf("dry-run should not create/update, got created=%d updated=%d", result.Created, result.Updated)
	}
	if len(*store) != 0 {
		t.Errorf("dry-run should not modify store, got %d entries", len(*store))
	}
	if !strings.Contains(buf.String(), "[dry-run]") {
		t.Errorf("output should contain [dry-run], got: %s", buf.String())
	}
}

func TestImporterCommentsAndBlankLines(t *testing.T) {
	t.Parallel()

	desc, _ := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `// This is a comment
{"__typename": "Location", "name": "A"}

// Another comment

{"__typename": "Location", "name": "B"}
`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 {
		t.Errorf("Created = %d, want 2", result.Created)
	}
}

func TestImporterNonExistentFile(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	_, err := imp.ImportFiles(context.Background(), []string{"/tmp/does-not-exist-12345.jsonl"})
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestImporterEmptyFile(t *testing.T) {
	t.Parallel()

	desc, _ := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", "")

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 && result.Updated != 0 {
		t.Errorf("empty file should produce no operations")
	}
}

func TestImporterNestedJSONValues(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "Location", "name": "A", "data": {"nested": {"deep": [1, 2, 3]}, "flag": true}}
`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Fatal("expected 1 created")
	}
	data, ok := (*store)[0]["data"].(map[string]any)
	if !ok {
		t.Fatalf("data field type = %T, want map[string]any", (*store)[0]["data"])
	}
	nested, ok := data["nested"].(map[string]any)
	if !ok {
		t.Fatal("nested field missing")
	}
	deep, ok := nested["deep"].([]any)
	if !ok || len(deep) != 3 {
		t.Errorf("deep = %v, want [1,2,3]", nested["deep"])
	}
}

func TestImporterUnicodeValues(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl",
		`{"__typename": "Location", "name": "日本語テスト", "data": {"emoji": "🏭", "chinese": "仓库"}}`) //nolint:gosmopolitan // intentionally testing unicode handling

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Fatal("expected 1 created")
	}
	if (*store)[0]["name"] != "日本語テスト" { //nolint:gosmopolitan // intentionally testing unicode handling
		t.Errorf("name = %v, want Japanese text", (*store)[0]["name"])
	}

	// Second run: update with different unicode values.
	buf.Reset()
	path2 := writeJSONL(t, dir, "test2.jsonl",
		`{"__typename": "Location", "name": "日本語テスト", "data": {"emoji": "🔧", "korean": "창고"}}`) //nolint:gosmopolitan // intentionally testing unicode handling

	result2, err := imp.ImportFiles(context.Background(), []string{path2})
	if err != nil {
		t.Fatal(err)
	}
	if result2.Updated != 1 {
		t.Errorf("Updated = %d, want 1", result2.Updated)
	}
	data, ok := (*store)[0]["data"].(map[string]any)
	if !ok {
		t.Fatal("data field not a map")
	}
	if data["emoji"] != "🔧" {
		t.Errorf("emoji = %v, want 🔧", data["emoji"])
	}
	if data["korean"] != "창고" {
		t.Errorf("korean = %v, want 창고", data["korean"])
	}
}

func TestImporterHTMLInjection(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "Location", "name": "<script>alert('xss')</script>"}`)

	// Should import without error — validation is the server's responsibility.
	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Fatal("expected 1 created")
	}
	if (*store)[0]["name"] != "<script>alert('xss')</script>" {
		t.Error("HTML should be preserved as-is")
	}
}

func TestImporterUTF8BOM(t *testing.T) {
	t.Parallel()

	desc, _ := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"__typename": "Location", "name": "A"}`+"\n")...)
	path := filepath.Join(dir, "bom.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := imp.ImportFiles(context.Background(), []string{path})
	if err == nil {
		t.Fatal("expected error for UTF-8 BOM prefix")
	}
}

func TestImporterInvalidUTF8Sequences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content []byte
	}{
		{"overlong", []byte(`{"__typename": "L", "name": "` + "\xC0\xAF" + `"}` + "\n")},
		{"surrogate half", []byte(`{"__typename": "L", "name": "` + "\xED\xA0\x80" + `"}` + "\n")},
		{"truncated 2-byte", []byte(`{"__typename": "L", "name": "` + "\xC2" + `"}` + "\n")},
		{"continuation without start", []byte(`{"__typename": "` + "\x80\x81" + `"}` + "\n")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := importexport.NewRegistry()
			var buf bytes.Buffer
			imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

			dir := t.TempDir()
			path := filepath.Join(dir, "test.jsonl")
			if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := imp.ImportFiles(context.Background(), []string{path})
			if err == nil {
				t.Error("expected error for invalid UTF-8 input")
			}
		})
	}
}

func TestImporterEmojiInValues(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))

	dir := t.TempDir()
	path := writeJSONL(t, dir, "test.jsonl", `{"__typename": "Location", "name": "🏭 Factory", "data": {"emoji": "👨‍👩‍👧‍👦", "flag": "🏳️‍🌈"}}
`)

	result, err := imp.ImportFiles(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Fatal("expected 1 created")
	}
	if (*store)[0]["name"] != "🏭 Factory" {
		t.Errorf("name = %v, want emoji name", (*store)[0]["name"])
	}
}

// FuzzParseRecord tests the JSON parser with random input to find panics
// or unexpected crashes.
func FuzzParseRecord(f *testing.F) {
	// Seed corpus with interesting inputs.
	f.Add([]byte(`{"__typename": "Location", "name": "A"}`))
	f.Add([]byte(`{"__typename": "", "name": "A"}`))
	f.Add([]byte(`{"name": "A"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`42`))
	f.Add([]byte(`true`))
	f.Add([]byte(``))
	f.Add([]byte(`{not json}`))
	f.Add([]byte{0x00, 0xFF, 0xFE})
	f.Add([]byte(`{"__typename": 123}`))
	f.Add([]byte(`{"__typename": null}`))
	f.Add([]byte(`{"__typename": ["array"]}`))
	f.Add([]byte(`{"__typename": "X", "data": {"$ref": {"__typename": "Y", "name": "Z"}}}`))
	f.Add([]byte(`{"__typename": "Location", "name": "` + strings.Repeat("A", 10000) + `"}`))
	f.Add([]byte(`{"__typename": "Location", "name": "\u0000\u0001\u001f"}`))
	f.Add([]byte(`{"__typename": "Location", "name": "test\x80\x81"}`))

	// BOM variants (UTF-8, UTF-16 LE, UTF-16 BE).
	f.Add(append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"__typename": "Location", "name": "bom-utf8"}`)...))
	f.Add(append([]byte{0xFF, 0xFE}, []byte(`{"__typename": "Location"}`)...))
	f.Add(append([]byte{0xFE, 0xFF}, []byte(`{"__typename": "Location"}`)...))

	// Invalid UTF-8 sequences.
	f.Add([]byte(`{"__typename": "Location", "name": "` + "\xC0\xAF" + `"}`))         // overlong
	f.Add([]byte(`{"__typename": "Location", "name": "` + "\xED\xA0\x80" + `"}`))     // surrogate half
	f.Add([]byte(`{"__typename": "Location", "name": "` + "\xF4\x90\x80\x80" + `"}`)) // above U+10FFFF
	f.Add([]byte(`{"__typename": "Location", "name": "` + "\xC2" + `"}`))             // truncated 2-byte
	f.Add([]byte(`{"__typename": "Location", "name": "` + "\xE0\x80" + `"}`))         // truncated 3-byte
	f.Add([]byte(`{"__typename": "` + "\x80\x81\x82" + `"}`))                         // continuation bytes without start

	// Emoji and special Unicode.
	f.Add([]byte(`{"__typename": "Location", "name": "🏭🔧📦"}`))
	f.Add([]byte(`{"__typename": "Location", "name": "👨‍👩‍👧‍👦"}`))           // family ZWJ sequence
	f.Add([]byte(`{"__typename": "Location", "name": "🏳️‍🌈"}`))              // flag ZWJ
	f.Add([]byte(`{"__typename": "Location", "name": "café"}`))              // combining accent
	f.Add([]byte(`{"__typename": "Location", "name": "\u202Ehello\u202C"}`)) // RTL override
	f.Add([]byte(`{"__typename": "Location", "name": "a\u0300"}`))           // combining grave
	f.Add([]byte(`{"__typename": "Location", "name": "\uFFFD"}`))            // replacement char
	f.Add([]byte(`{"__typename": "Location", "name": "\uFEFF"}`))            // zero-width no-break space
	f.Add([]byte(`{"__typename": "Location", "name": "\u200B"}`))            // zero-width space
	f.Add([]byte(`{"__typename": "Location", "name": "\u0000"}`))            // null char in JSON escape

	f.Fuzz(func(t *testing.T, data []byte) {
		// ParseRecord must never panic, regardless of input.
		// Errors are expected and fine — panics are not.
		_, _ = importexport.ParseRecord(data)
	})
}

// TestImporterRefusesImmutableFieldChange guards the silent-drop this exists to
// prevent: the update input cannot carry an immutable field, so without the
// check the record would report as updated while that one difference vanished.
func TestImporterRefusesImmutableFieldChange(t *testing.T) {
	t.Parallel()

	desc, store := fakeDescriptor("Location")
	desc.ImmutableFields = []string{"kind"}
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))
	dir := t.TempDir()

	create := writeJSONL(t, dir, "create.jsonl",
		`{"__typename": "Location", "name": "A", "kind": "shelf", "data": "x"}`+"\n")
	if _, err := imp.ImportFiles(context.Background(), []string{create}); err != nil {
		t.Fatal(err)
	}

	// Same kind: the update goes through.
	same := writeJSONL(t, dir, "same.jsonl",
		`{"__typename": "Location", "name": "A", "kind": "shelf", "data": "y"}`+"\n")
	res, err := imp.ImportFiles(context.Background(), []string{same})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Errorf("Updated = %d, want 1", res.Updated)
	}

	// Different kind: refused, and the stored entity is left alone.
	changed := writeJSONL(t, dir, "changed.jsonl",
		`{"__typename": "Location", "name": "A", "kind": "pallet", "data": "z"}`+"\n")
	if _, err := imp.ImportFiles(context.Background(), []string{changed}); err == nil {
		t.Fatal("expected the import to be refused")
	} else if !errors.Is(err, importexport.ErrImmutableField) {
		t.Errorf("error = %q, want ErrImmutableField", err)
	}
	if (*store)[0]["data"] != "y" {
		t.Errorf("data = %v, want 'y' — the refused import must not apply the rest", (*store)[0]["data"])
	}
}

// TestImporterRefusesImmutableFieldChangeOnCreateOnly covers the skip path: a
// create-only entity (DataType) that already exists under its identity is
// skipped, and a skip would drop a changed fixed-at-creation field just as
// silently as an update would.
func TestImporterRefusesImmutableFieldChangeOnCreateOnly(t *testing.T) {
	t.Parallel()

	desc, store := fakeCompositeDescriptor("DataType", "slug", "version")
	desc.ImmutableFields = []string{"jsonSchema"}
	reg := importexport.NewRegistry()
	if err := reg.Register(desc); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	imp := importexport.NewImporter(reg, importexport.WithOutput(&buf))
	dir := t.TempDir()

	create := writeJSONL(t, dir, "create.jsonl",
		`{"__typename": "DataType", "slug": "item", "version": 1, "jsonSchema": "{}"}`+"\n")
	if _, err := imp.ImportFiles(context.Background(), []string{create}); err != nil {
		t.Fatal(err)
	}

	// Same schema: skipped as already existing.
	same := writeJSONL(t, dir, "same.jsonl",
		`{"__typename": "DataType", "slug": "item", "version": 1, "jsonSchema": "{}"}`+"\n")
	res, err := imp.ImportFiles(context.Background(), []string{same})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", res.Skipped)
	}

	// Different schema under the same (slug, version): refused, not skipped.
	changed := writeJSONL(t, dir, "changed.jsonl",
		`{"__typename": "DataType", "slug": "item", "version": 1, "jsonSchema": "{\"type\":\"object\"}"}`+"\n")
	if _, err := imp.ImportFiles(context.Background(), []string{changed}); err == nil {
		t.Fatal("expected the import to be refused")
	} else if !errors.Is(err, importexport.ErrImmutableField) {
		t.Errorf("error = %q, want ErrImmutableField", err)
	}
	if len(*store) != 1 {
		t.Errorf("store has %d entities, want 1 — the refused import must not create a duplicate", len(*store))
	}
}

// A directory produced by ExportToDir must be importable by passing the
// directory itself: the exporter names files after their type, so plain
// alphabetical expansion feeds a referrer to the importer before its
// reference target ("customer.jsonl" sorts before "datatype.jsonl"), and the
// $ref cannot resolve. Directory expansion must follow the registry's
// dependency order instead.
func TestImporterDirectoryExpandsInDependencyOrder(t *testing.T) {
	t.Parallel()

	var order []string
	record := func(typeName string) *importexport.EntityDescriptor {
		desc, _ := fakeCompositeDescriptor(typeName, "name")
		inner := desc.Create
		desc.Create = func(ctx context.Context, input map[string]any) (map[string]any, error) {
			order = append(order, typeName)
			return inner(ctx, input)
		}
		return desc
	}

	dataType := record("DataType")
	customer := record("Customer")
	customer.References = []importexport.Reference{{Field: "dataTypeID", TargetType: "DataType"}}

	reg := importexport.NewRegistry()
	for _, d := range []*importexport.EntityDescriptor{customer, dataType} {
		if err := reg.Register(d); err != nil {
			t.Fatal(err)
		}
	}

	// File names as ExportToDir writes them.
	dir := t.TempDir()
	writeJSONL(t, dir, "customer.jsonl", `{"__typename":"Customer","name":"c1"}`)
	writeJSONL(t, dir, "datatype.jsonl", `{"__typename":"DataType","name":"widget"}`)

	imp := importexport.NewImporter(reg, importexport.WithOutput(&bytes.Buffer{}))
	result, err := imp.ImportFiles(context.Background(), []string{dir})
	if err != nil {
		t.Fatalf("import directory: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected import errors: %v", result.Errors)
	}

	want := []string{"DataType", "Customer"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("processing order = %v, want %v (target before referrer)", order, want)
	}
}
