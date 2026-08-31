package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// occurrence is one regex match of a Ref's pattern against one line of one
// file: the raw captured string plus enough position information to both
// report a useful error and, in --fix mode, splice a replacement back in.
type occurrence struct {
	file      string
	line      int    // 1-based
	raw       string // the captured group, verbatim
	spanStart int    // byte offset of the captured group within the file
	spanEnd   int    // byte offset one past the captured group
}

// resolveFiles expands a Ref to the list of tracked files it applies to:
// either its single explicit File, or every file `git ls-files` returns
// for its Glob pathspec. Mirrors the old scripts' `git ls-files
// "${PATTERNS[@]}"` discovery so file selection behavior is unchanged.
func resolveFiles(repoRoot string, r Ref) ([]string, error) {
	if r.File != "" {
		full := repoRoot + "/" + r.File
		if _, err := os.Stat(full); err != nil {
			return nil, fmt.Errorf("file %s: %w", r.File, err)
		}
		return []string{r.File}, nil
	}

	cmd := exec.Command("git", "ls-files", "--", r.Glob)
	cmd.Dir = repoRoot
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files %q: %w (%s)", r.Glob, err, strings.TrimSpace(stderr.String()))
	}

	var files []string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// git ls-files can list paths for deletions staged-but-uncommitted
		// in exotic states; skip anything not actually present on disk,
		// matching the old scripts' `[[ -f "$file" ]] || continue` guard.
		if _, err := os.Stat(repoRoot + "/" + line); err != nil {
			continue
		}
		files = append(files, line)
	}
	sort.Strings(files)
	return files, nil
}

// findOccurrences reads every file in files and returns every line-level
// match of r's compiled pattern, in file order and then position order.
func findOccurrences(repoRoot string, r Ref, files []string) ([]occurrence, error) {
	var occs []occurrence
	for _, f := range files {
		data, err := os.ReadFile(repoRoot + "/" + f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}

		matches := r.compiled.FindAllSubmatchIndex(data, -1)
		for _, m := range matches {
			// m[0], m[1] = whole match; m[2], m[3] = capture group 1.
			start, end := m[2], m[3]
			occs = append(occs, occurrence{
				file:      f,
				line:      1 + bytes.Count(data[:start], []byte("\n")),
				raw:       string(data[start:end]),
				spanStart: start,
				spanEnd:   end,
			})
		}
	}
	return occs, nil
}
