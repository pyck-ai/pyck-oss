package importexport_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

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
