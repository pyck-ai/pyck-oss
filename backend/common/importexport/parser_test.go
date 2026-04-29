package importexport_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

// collect drains a StreamFiles iterator into a slice for testing.
func collect(paths []string) ([]importexport.ImportRecord, error) {
	var records []importexport.ImportRecord
	for record, err := range importexport.StreamFiles(paths) {
		if err != nil {
			return records, err
		}
		records = append(records, record)
	}
	return records, nil
}

func TestStreamJSONL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "entities.jsonl")
	content := `{"__typename": "Location", "name": "Building-A", "data": {"zone": "north"}}
{"__typename": "Repository", "name": "Shelf-1", "parentID": {"$ref": {"__typename": "Repository", "name": "Warehouse-A"}}}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	records, err := collect([]string{path})
	require.NoError(t, err)
	require.Len(t, records, 2)

	assert.Equal(t, "Location", records[0].TypeName)
	assert.Equal(t, "Building-A", records[0].Data["name"])
	assert.Equal(t, path, records[0].Source)
	assert.Equal(t, 1, records[0].Line)

	assert.Equal(t, "Repository", records[1].TypeName)
	assert.Equal(t, "Shelf-1", records[1].Data["name"])
	assert.Equal(t, 2, records[1].Line)

	// Verify $ref is preserved as-is in the data.
	ref, ok := records[1].Data["parentID"].(map[string]any)
	require.True(t, ok)
	refTarget, ok := ref["$ref"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Repository", refTarget["__typename"])
	assert.Equal(t, "Warehouse-A", refTarget["name"])
}

func TestStreamRefID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "refid.jsonl")
	content := `{"__typename": "Customer", "$refid": "customer-acme", "data": {"name": "Acme"}}
{"__typename": "PickingOrder", "customerID": {"$ref": "customer-acme"}}
{"__typename": "Location", "name": "Building-A"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	records, err := collect([]string{path})
	require.NoError(t, err)
	require.Len(t, records, 3)

	// $refid is extracted and removed from data.
	assert.Equal(t, "customer-acme", records[0].RefID)
	assert.Nil(t, records[0].Data["$refid"], "$refid should be removed from data")

	// String $ref is preserved in data for the resolver.
	ref, ok := records[1].Data["customerID"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "customer-acme", ref["$ref"])

	// Records without $refid have empty RefID.
	assert.Empty(t, records[2].RefID)
}

func TestStreamDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create files in non-alphabetical order to verify sorting.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "02-repos.jsonl"),
		[]byte(`{"__typename": "Repository", "name": "Shelf-1"}`+"\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "01-locations.jsonl"),
		[]byte(`{"__typename": "Location", "name": "Building-A"}`+"\n"),
		0o600,
	))
	// Non-JSONL files should be ignored.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "readme.txt"),
		[]byte(`not jsonl`),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "data.json"),
		[]byte(`{"__typename": "Location", "name": "ignored"}`),
		0o600,
	))

	records, err := collect([]string{dir})
	require.NoError(t, err)
	require.Len(t, records, 2)

	// Alphabetical order.
	assert.Equal(t, "Location", records[0].TypeName)
	assert.Equal(t, "Repository", records[1].TypeName)
}

func TestStreamMissingTypename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"name": "no-typename"}`+"\n"), 0o600))

	_, err := collect([]string{path})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "__typename")
}

func TestStreamSkipsEmptyLinesAndComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "with-blanks.jsonl")
	content := `
// This is a comment
{"__typename": "Location", "name": "A"}

// Another comment
{"__typename": "Location", "name": "B"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	records, err := collect([]string{path})
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "A", records[0].Data["name"])
	assert.Equal(t, "B", records[1].Data["name"])
}

func TestStreamMultipleFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	file1 := filepath.Join(dir, "a.jsonl")
	require.NoError(t, os.WriteFile(file1,
		[]byte(`{"__typename": "Location", "name": "First"}`+"\n"), 0o600))

	file2 := filepath.Join(dir, "b.jsonl")
	require.NoError(t, os.WriteFile(file2,
		[]byte(`{"__typename": "Device", "name": "Second"}`+"\n"), 0o600))

	// Files processed in command-line order, not alphabetical.
	records, err := collect([]string{file2, file1})
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "Device", records[0].TypeName)
	assert.Equal(t, "Location", records[1].TypeName)
}

func TestStreamProcessesRecordsOneAtATime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "entities.jsonl")
	content := `{"__typename": "Location", "name": "A"}
{"__typename": "Location", "name": "B"}
{"__typename": "Location", "name": "C"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	// Verify we can break out of iteration early.
	count := 0
	for _, err := range importexport.StreamFiles([]string{path}) {
		require.NoError(t, err)
		count++
		if count == 2 {
			break
		}
	}
	assert.Equal(t, 2, count, "should stop after 2 records")
}

func TestStreamStopsOnNonexistentPath(t *testing.T) {
	t.Parallel()

	records, err := collect([]string{"/nonexistent/path.jsonl"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.Empty(t, records)
}

func TestStreamStopsOnMalformedJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	content := `{"__typename": "Location", "name": "A"}
{not valid json}
{"__typename": "Location", "name": "C"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	records, err := collect([]string{path})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON")
	assert.Contains(t, err.Error(), ":2:")
	// Only the first valid record should have been yielded before the error.
	assert.Len(t, records, 1)
	assert.Equal(t, "A", records[0].Data["name"])
}

func TestStreamStopsOnEmptyTypename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty-type.jsonl")
	content := `{"__typename": "", "name": "bad"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	_, err := collect([]string{path})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "__typename must be a non-empty string")
}

func TestStreamStopsOnMalformedThenSkipsRemainingFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	file1 := filepath.Join(dir, "a.jsonl")
	require.NoError(t, os.WriteFile(file1, []byte(`{"__typename": "Location", "name": "A"}
{"broken
`), 0o600))

	file2 := filepath.Join(dir, "b.jsonl")
	require.NoError(t, os.WriteFile(file2,
		[]byte(`{"__typename": "Location", "name": "B"}`+"\n"), 0o600))

	records, err := collect([]string{file1, file2})
	require.Error(t, err)
	// File2 should never be processed because file1 had an error.
	assert.Len(t, records, 1)
	assert.Equal(t, "A", records[0].Data["name"])
}

func TestStreamStopsOnNonJSONLFile(t *testing.T) {
	t.Parallel()

	// A .txt file is not a .jsonl file and not a directory — streaming it
	// fails because the content is not valid JSON.
	dir := t.TempDir()
	txtFile := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(txtFile, []byte("hello"), 0o600))

	_, err := collect([]string{txtFile})
	require.Error(t, err)
}
