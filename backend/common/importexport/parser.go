package importexport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StreamFiles returns an iterator that yields ImportRecords one at a time from
// the given paths. Each path can be a .jsonl file or a directory. Directories
// are expanded to their contained .jsonl files in C-locale alphabetical order.
// Files are processed in the order given on the command line.
//
// The iterator stops on the first error and yields it as the final value.
// Errors that stop iteration:
//   - path does not exist or is inaccessible
//   - directory cannot be read
//   - file cannot be opened
//   - I/O error while reading a file
//   - malformed JSON on a line
//   - missing or empty __typename field
func StreamFiles(paths []string) iter.Seq2[ImportRecord, error] {
	return func(yield func(ImportRecord, error) bool) {
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				yield(ImportRecord{}, err)
				return
			}

			var files []string
			if info.IsDir() {
				files, err = expandDirectory(path)
				if err != nil {
					yield(ImportRecord{}, err)
					return
				}
			} else {
				files = []string{path}
			}

			for _, file := range files {
				if !streamJSONL(file, yield) {
					return
				}
			}
		}
	}
}

// expandDirectory returns the absolute paths of all .jsonl files in dir,
// sorted alphabetically by filename. Subdirectories and non-.jsonl files are
// ignored.
func expandDirectory(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	// Sort entries by name in C locale (byte order, which is Go's default).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.ToLower(filepath.Ext(entry.Name())) == ".jsonl" {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files, nil
}

// streamJSONL reads a JSONL file line by line and yields records. Empty lines
// and lines starting with // are skipped. Individual lines are limited to 1MB;
// longer lines produce a scanner error. Returns false if the consumer stopped
// iteration.
func streamJSONL(path string, yield func(ImportRecord, error) bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return yield(ImportRecord{}, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		record, err := parseRecord([]byte(line))
		if err != nil {
			yield(ImportRecord{}, fmt.Errorf("%s:%d: %w", path, lineNum, err))
			return false
		}
		record.Source = path
		record.Line = lineNum

		if !yield(record, nil) {
			return false
		}
	}

	if err := scanner.Err(); err != nil {
		return yield(ImportRecord{}, err)
	}
	return true
}

// parseRecord unmarshals a single JSON object into an ImportRecord. It
// extracts and removes the __typename field from the data, returning an error
// if __typename is missing or empty.
func parseRecord(data []byte) (ImportRecord, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ImportRecord{}, fmt.Errorf("invalid JSON: %w", err)
	}

	typeName, ok := raw["__typename"]
	if !ok {
		return ImportRecord{}, ErrMissingTypename
	}

	typeNameStr, ok := typeName.(string)
	if !ok || typeNameStr == "" {
		return ImportRecord{}, ErrInvalidTypeName
	}

	delete(raw, "__typename")

	// Extract optional $refid local alias.
	var refID string
	if v, ok := raw["$refid"].(string); ok {
		refID = v
	}
	delete(raw, "$refid")

	return ImportRecord{
		TypeName: typeNameStr,
		Data:     raw,
		RefID:    refID,
	}, nil
}
