package checker

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverLimitMixinEntities walks root looking for **/ent/schema/*.go
// files and returns the set of entity type names whose Mixin() method
// embeds mixin.LimitMixin. Entity names are returned exactly as
// declared in the schema (e.g., "Item", "ReplenishmentOrder").
//
// The scan is intentionally simple — it does not type-check the schema
// package. It identifies a struct as a LimitMixin entity when:
//
//   - the struct is declared in a file whose directory ends in /ent/schema
//   - the file has a method on that struct whose name is "Mixin"
//   - the method body lexically mentions "LimitMixin"
//
// This matches every existing schema file in the pyck repo and avoids
// chicken-and-egg compile dependencies (schema packages import the
// real mixin package; the linter does not need to).
func DiscoverLimitMixinEntities(root string) (map[string]bool, error) {
	out := map[string]bool{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "out" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		dir := filepath.Dir(path)
		if filepath.Base(dir) != "schema" || filepath.Base(filepath.Dir(dir)) != "ent" {
			return nil
		}
		entities, parseErr := limitMixinEntitiesInFile(fset, path)
		if parseErr != nil {
			// A single bad schema file shouldn't abort the whole walk;
			// surface the error but continue. The discovery is best-effort.
			fmt.Fprintf(os.Stderr, "limitlint: parse %s: %v\n", path, parseErr)
			return nil
		}
		for _, e := range entities {
			out[e] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func limitMixinEntitiesInFile(fset *token.FileSet, path string) ([]string, error) {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		if fn.Name.Name != "Mixin" {
			continue
		}
		recvType := receiverTypeName(fn.Recv.List[0].Type)
		if recvType == "" {
			continue
		}
		if !bodyMentionsLimitMixin(fn.Body) {
			continue
		}
		out = append(out, recvType)
	}
	return out, nil
}

func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		if ident, ok := e.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

// bodyMentionsLimitMixin scans a function body for any selector or
// identifier that resolves to "LimitMixin". This is a deliberately
// lexical check — sufficient for the pyck convention where Mixin()
// returns []ent.Mixin{ mixin.LimitMixin{}, ... }.
func bodyMentionsLimitMixin(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if v.Sel != nil && v.Sel.Name == "LimitMixin" {
				found = true
				return false
			}
		case *ast.Ident:
			if v.Name == "LimitMixin" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
