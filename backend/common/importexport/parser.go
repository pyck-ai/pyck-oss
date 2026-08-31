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
// the given paths. Each path can be a .jsonl file or a directory. Files are
// processed in the order given on the command line.
//
// A directory is expanded to its .jsonl files ordered by typeOrder, matching
// them on the "<lowercase typename>.jsonl" names ExportToDir writes. Records
// stream straight into the importer, which resolves a $ref by querying its
// target, so a referrer file read before its target's file cannot resolve —
// callers pass Registry.TypeNamesInDependencyOrder. Files whose name matches
// no entry, and the whole directory when typeOrder is nil, fall back to
// C-locale alphabetical order.
//
// The iterator stops on the first error and yields it as the final value.
// Errors that stop iteration:
//   - path does not exist or is inaccessible
//   - directory cannot be read
//   - file cannot be opened
//   - I/O error while reading a file
//   - malformed JSON on a line
//   - missing or empty __typename field
func StreamFiles(paths []string, typeOrder []string) iter.Seq2[ImportRecord, error] {
	return func(yield func(ImportRecord, error) bool) {
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				yield(ImportRecord{}, err)
				return
			}

			var files []string
			if info.IsDir() {
				files, err = expandDirectory(path, typeOrder)
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

// expandDirectory returns the paths of all .jsonl files in dir, ordered by
// typeOrder (see StreamFiles) with unmatched names last in C-locale
// alphabetical order. Subdirectories and non-.jsonl files are ignored.
func expandDirectory(dir string, typeOrder []string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	// Rank by the position of the file's type in typeOrder; names that match
	// no registered type sort after every known one.
	rank := make(map[string]int, len(typeOrder))
	for i, typeName := range typeOrder {
		rank[strings.ToLower(typeName)+".jsonl"] = i
	}
	rankOf := func(name string) int {
		if i, ok := rank[strings.ToLower(name)]; ok {
			return i
		}
		return len(typeOrder)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.ToLower(filepath.Ext(entry.Name())) == ".jsonl" {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}

	sort.SliceStable(files, func(i, j int) bool {
		ri, rj := rankOf(filepath.Base(files[i])), rankOf(filepath.Base(files[j]))
		if ri != rj {
			return ri < rj
		}
		// Byte order, which is Go's default string comparison.
		return files[i] < files[j]
	})
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
