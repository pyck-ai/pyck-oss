package main

import (
	"errors"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	ent "entgo.io/ent/entc/gen"
)

// jsonbRenameHook returns a gen.Hook that post-processes the generated
// gql_pagination.go file using go/ast to rename methods that our custom
// JSONB pagination template will override.
func jsonbRenameHook() ent.Hook {
	return func(next ent.Generator) ent.Generator {
		return ent.GenerateFunc(func(g *ent.Graph) error {
			if err := next.Generate(g); err != nil {
				return err
			}
			return postProcessPagination(g.Target)
		})
	}
}

// pagerMethodsToRename is the set of Pager methods that get prefixed with "ent_".
var pagerMethodsToRename = map[string]bool{
	"toCursor":     true,
	"applyCursors": true,
	"applyOrder":   true,
	"orderExpr":    true,
}

// postProcessPagination reads gql_pagination.go from targetDir, applies AST
// transformations (rename methods, add struct fields), and writes it back.
func postProcessPagination(targetDir string) error {
	path := filepath.Join(targetDir, "gql_pagination.go")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil // no GraphQL pagination file → nothing to do
	} else if err != nil {
		return err
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	renamePagerMethods(file)
	renameWithOrderFuncs(file)
	addOrderStructFields(file)

	var buf strings.Builder
	if err := format.Node(&buf, fset, file); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buf.String()), 0o600)
}

// renamePagerMethods prefixes Pager receiver methods (toCursor, applyCursors,
// applyOrder, orderExpr) with "ent_" so our custom template can override them.
func renamePagerMethods(file *ast.File) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		if !pagerMethodsToRename[fn.Name.Name] {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if !ok {
			continue
		}
		if strings.HasSuffix(ident.Name, "Pager") {
			fn.Name.Name = "ent_" + fn.Name.Name
		}
	}
}

// renameWithOrderFuncs prefixes top-level With*Order functions with "ent_".
func renameWithOrderFuncs(file *ast.File) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if strings.HasPrefix(fn.Name.Name, "With") && strings.HasSuffix(fn.Name.Name, "Order") {
			fn.Name.Name = "ent_" + fn.Name.Name
		}
	}
}

// addOrderStructFields appends JSONPath and JSONType fields to *Order structs
// that have both Direction and Field fields.
func addOrderStructFields(file *ast.File) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(ts.Name.Name, "Order") {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			if !hasField(st, "Direction") || !hasField(st, "Field") {
				continue
			}
			st.Fields.List = append(st.Fields.List,
				&ast.Field{
					Names: []*ast.Ident{ast.NewIdent("JSONPath")},
					Type:  &ast.StarExpr{X: ast.NewIdent("string")},
					Tag:   &ast.BasicLit{Kind: token.STRING, Value: "`json:\"jsonPath,omitempty\"`"},
				},
				&ast.Field{
					Names: []*ast.Ident{ast.NewIdent("JSONType")},
					Type:  &ast.StarExpr{X: ast.NewIdent("JSONType")},
					Tag:   &ast.BasicLit{Kind: token.STRING, Value: "`json:\"jsonType,omitempty\"`"},
				},
			)
		}
	}
}

// hasField reports whether the struct has a field with the given name.
func hasField(st *ast.StructType, name string) bool {
	for _, f := range st.Fields.List {
		for _, id := range f.Names {
			if id.Name == name {
				return true
			}
		}
	}
	return false
}
