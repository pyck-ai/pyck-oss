package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// services lists every module whose ent code is generated with the templates
// under templates/ent, relative to this package.
var services = []string{"file", "inventory", "main-data", "management", "picking", "receiving", "workflow"}

// TestGeneratedRelationPredicatesCarryNeighborScope walks every service's
// generated where.go and checks the templates' contract: a Has<Edge>With
// appends predicate.ScopeNeighborToTenants to the neighbour sub-select exactly
// when the target entity carries tenant_id, and predicate.ScopeNeighborToLive
// exactly when it carries deleted_at. A plain Has<Edge>() carries the same
// scopes, whichever side owns the foreign key. It runs on the committed
// generated code, so a template regression fails here without a live stack.
func TestGeneratedRelationPredicatesCarryNeighborScope(t *testing.T) {
	t.Parallel()

	checked, checkedPlain := 0, 0
	for _, svc := range services {
		genDir := filepath.Join("..", "..", "..", svc, "ent", "gen")
		files, err := filepath.Glob(filepath.Join(genDir, "*", "where.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("%s: no generated where.go under %s (run task generate)", svc, genDir)
		}

		tenantScoped := map[string]bool{}  // predicate type name → target has tenant_id
		historyScoped := map[string]bool{} // predicate type name → target has deleted_at
		parsed := map[string]*ast.File{}
		fset := token.NewFileSet()
		for _, f := range files {
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			parsed[f] = file
			tenantScoped[predicateName(file)] = hasFunc(file, "TenantID")
			historyScoped[predicateName(file)] = hasFunc(file, "DeletedAt")
		}

		for f, file := range parsed {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !strings.HasPrefix(fn.Name.Name, "Has") || !strings.HasSuffix(fn.Name.Name, "With") {
					continue
				}
				target := predicateTypeOfParam(fn)
				if target == "" {
					continue
				}
				scoped := funcBodyContains(fset, fn, "predicate.ScopeNeighborToTenants")
				if want := tenantScoped[target]; scoped != want {
					t.Errorf("%s: %s targets %s (tenant-scoped=%v) but tenant scope present=%v", f, fn.Name.Name, target, want, scoped)
				}
				live := funcBodyContains(fset, fn, "predicate.ScopeNeighborToLive")
				if want := historyScoped[target]; live != want {
					t.Errorf("%s: %s targets %s (soft-deletable=%v) but live scope present=%v", f, fn.Name.Name, target, want, live)
				}
				checked++
			}
		}

		// Plain Has<Edge>() predicates: scoped like their Has<Edge>With sibling,
		// so hasX: true means hasXWith: {} on every edge kind.
		for f, file := range parsed {
			with := map[string]string{} // plain name → target predicate type
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && strings.HasPrefix(fn.Name.Name, "Has") && strings.HasSuffix(fn.Name.Name, "With") {
					if target := predicateTypeOfParam(fn); target != "" {
						with[strings.TrimSuffix(fn.Name.Name, "With")] = target
					}
				}
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Type.Params == nil || len(fn.Type.Params.List) != 0 {
					continue
				}
				target, ok := with[fn.Name.Name]
				if !ok {
					continue
				}
				scoped := funcBodyContains(fset, fn, "predicate.ScopeNeighborToTenants")
				if want := tenantScoped[target]; scoped != want {
					t.Errorf("%s: %s targets %s (tenant-scoped=%v) but tenant scope present=%v", f, fn.Name.Name, target, want, scoped)
				}
				live := funcBodyContains(fset, fn, "predicate.ScopeNeighborToLive")
				if want := historyScoped[target]; live != want {
					t.Errorf("%s: %s targets %s (soft-deletable=%v) but live scope present=%v", f, fn.Name.Name, target, want, live)
				}
				checkedPlain++
			}
		}
	}
	if checked == 0 || checkedPlain == 0 {
		t.Fatalf("no relation predicates found (with=%d, plain=%d)", checked, checkedPlain)
	}
	t.Logf("checked %d Has<Edge>With and %d Has<Edge>() predicates", checked, checkedPlain)
}

// TestGeneratedQueriesCarryContextOnTraversals checks the query template's
// contract on the committed generated code: sqlAll and sqlCount of every
// entity set the query context on an edge traversal's From selector, which
// ent v0.14.6 leaves without one. Without it the neighbour scopes read an
// anonymous caller on every edge connection and match nothing.
func TestGeneratedQueriesCarryContextOnTraversals(t *testing.T) {
	t.Parallel()

	checked := 0
	for _, svc := range services {
		files, err := filepath.Glob(filepath.Join("..", "..", "..", svc, "ent", "gen", "*_query.go"))
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		for _, f := range files {
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || (fn.Name.Name != "sqlAll" && fn.Name.Name != "sqlCount") {
					continue
				}
				if !funcBodyContains(fset, fn, "_spec.From.WithContext(ctx)") {
					t.Errorf("%s: %s does not set the context on _spec.From", f, fn.Name.Name)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no generated sqlAll/sqlCount found (run task generate)")
	}
	t.Logf("checked %d sqlAll/sqlCount functions", checked)
}

// TestGeneratedUpdateOneCarriesContextToPredicates checks the update
// template's contract on the committed generated code: the sqlSave of every
// UpdateOne builder runs its predicates on a selector carrying the context,
// which ent v0.14.6 builds without one. Without it the neighbour scopes read
// an anonymous caller and UpdateOne with a relation predicate fails with not
// found.
func TestGeneratedUpdateOneCarriesContextToPredicates(t *testing.T) {
	t.Parallel()

	checked := 0
	for _, svc := range services {
		files, err := filepath.Glob(filepath.Join("..", "..", "..", svc, "ent", "gen", "*_update.go"))
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		for _, f := range files {
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != "sqlSave" || !strings.HasSuffix(receiverType(fn), "UpdateOne") {
					continue
				}
				if !funcBodyContains(fset, fn, "pred(s.WithContext(ctx))") {
					t.Errorf("%s: %s.sqlSave does not run its predicates with the context", f, receiverType(fn))
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no generated UpdateOne sqlSave found (run task generate)")
	}
	t.Logf("checked %d UpdateOne sqlSave functions", checked)
}

// receiverType returns T for a method with receiver `*T` or `T`.
func receiverType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return ""
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// predicateName returns the entity name of a where.go, taken from the
// `predicate.<Entity>` return type of its ID predicate.
func predicateName(file *ast.File) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ID" || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if sel, ok := fn.Type.Results.List[0].Type.(*ast.SelectorExpr); ok {
			return sel.Sel.Name
		}
	}
	return ""
}

// predicateTypeOfParam returns X for a variadic `preds ...predicate.X` parameter.
func predicateTypeOfParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return ""
	}
	ell, ok := fn.Type.Params.List[0].Type.(*ast.Ellipsis)
	if !ok {
		return ""
	}
	sel, ok := ell.Elt.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "predicate" {
		return ""
	}
	return sel.Sel.Name
}

func hasFunc(file *ast.File, name string) bool {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return true
		}
	}
	return false
}

// funcBodyContains reports whether the source text of fn's body contains needle.
func funcBodyContains(fset *token.FileSet, fn *ast.FuncDecl, needle string) bool {
	pos := fset.Position(fn.Body.Pos())
	end := fset.Position(fn.Body.End())
	src, err := os.ReadFile(pos.Filename)
	if err != nil {
		return false
	}
	return strings.Contains(string(src[pos.Offset:end.Offset]), needle)
}
