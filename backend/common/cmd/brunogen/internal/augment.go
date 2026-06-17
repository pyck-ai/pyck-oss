// Package gen — augment.go re-adds GraphQL selection fields that apigen omits
// (reverse-Connection wrappers) when a fixture assertion references them.
package gen

import (
	"regexp"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

// refBracketRE matches any `[...]` index suffix on a path segment so the
// selection-set walker sees the bare field name. Refs use both `edges[]`
// (any element) and `edges[0]` (specific index); both collapse to `edges`.
var refBracketRE = regexp.MustCompile(`\[[^]]*\]`)

// AugmentOperationContent re-emits opContent with extra selections added so
// every response path referenced by the step's assertions resolves to a real
// field. Returns opContent unchanged if no augmentation is needed or if the
// operation fails to parse (the assertion will then fail loudly with the
// usual "expected undefined" message, which is still a better signal than
// silent miscompile here).
//
// Motivation: apigen deliberately stops at reverse-Connection wrappers
// (Item.items, Repository.children, …) to keep default queries bounded. Test
// fixtures still assert through those wrappers (X.items.totalCount); the gap
// makes those assertions silently see undefined.
func AugmentOperationContent(opContent string, assertions []types.TestAssertion) string {
	paths := collectResponsePaths(assertions)
	if len(paths) == 0 {
		return opContent
	}

	doc, err := parser.ParseQuery(&ast.Source{Input: opContent})
	if err != nil || len(doc.Operations) == 0 {
		return opContent
	}

	op := doc.Operations[0]
	changed := false
	for _, path := range paths {
		if len(path) == 0 {
			continue
		}
		if augmentSelectionSet(&op.SelectionSet, path) {
			changed = true
		}
	}
	if !changed {
		return opContent
	}

	var sb strings.Builder
	formatter.NewFormatter(&sb, formatter.WithIndent("  ")).FormatQueryDocument(doc)
	return strings.TrimRight(sb.String(), "\n")
}

// collectResponsePaths flattens each assertion ref into a selection-path slice
// rooted at the operation's top-level result field. Refs that don't target the
// response body (req.body.*, res.status, res.headers.*) are skipped.
func collectResponsePaths(assertions []types.TestAssertion) [][]string {
	var paths [][]string
	for _, block := range assertions {
		for _, a := range block.Assertions {
			if path := parseResponseRef(a.Ref); len(path) > 0 {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// parseResponseRef strips the res.body.data. prefix and splits the remainder
// into selection-set segments. `edges[]` collapses to `edges` (the bracket
// only documents the list shape; the selection-set name is the same).
func parseResponseRef(ref string) []string {
	const prefix = "res.body.data."
	if !strings.HasPrefix(ref, prefix) {
		return nil
	}
	rest := refBracketRE.ReplaceAllString(ref[len(prefix):], "")
	parts := strings.Split(rest, ".")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// augmentSelectionSet walks `sel` following path. Existing fields are
// traversed; missing fields are inserted as bare selections so the formatter
// emits `field` for scalar leaves and `field { ... }` once the recursion
// nests deeper. Returns true if any addition was made.
func augmentSelectionSet(sel *ast.SelectionSet, path []string) bool {
	if len(path) == 0 {
		return false
	}
	head, tail := path[0], path[1:]

	for _, s := range *sel {
		f, ok := s.(*ast.Field)
		if !ok {
			continue
		}
		if f.Name == head {
			if len(tail) == 0 {
				return false
			}
			// A parsed field with an empty SelectionSet is a scalar leaf
			// (object fields require selections in valid GraphQL, so the
			// parser only accepts them with `{ ... }`). Remaining ref
			// segments are JSON property accesses on the scalar value —
			// not GraphQL sub-selections to add.
			if len(f.SelectionSet) == 0 {
				return false
			}
			return augmentSelectionSet(&f.SelectionSet, tail)
		}
	}

	newField := &ast.Field{Name: head}
	*sel = append(*sel, newField)
	if len(tail) > 0 {
		augmentSelectionSet(&newField.SelectionSet, tail)
	}
	return true
}
