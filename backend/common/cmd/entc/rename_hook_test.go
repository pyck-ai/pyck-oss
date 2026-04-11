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

func TestRenamePagerMethods(t *testing.T) {
	t.Parallel()

	src := `package gen

type customerPager struct{}

func (p *customerPager) toCursor(c *Customer) Cursor { return Cursor{} }
func (p *customerPager) applyCursors(q *CustomerQuery, after, before *Cursor) (*CustomerQuery, error) { return q, nil }
func (p *customerPager) applyOrder(q *CustomerQuery) *CustomerQuery { return q }
func (p *customerPager) orderExpr(q *CustomerQuery) Querier { return nil }
func (p *customerPager) applyFilter(q *CustomerQuery) (*CustomerQuery, error) { return q, nil }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	renamePagerMethods(file)

	names := funcNames(file)

	for _, want := range []string{"ent_toCursor", "ent_applyCursors", "ent_applyOrder", "ent_orderExpr"} {
		if !names[want] {
			t.Errorf("expected %s to be present", want)
		}
	}

	// applyFilter should NOT be renamed.
	if names["ent_applyFilter"] {
		t.Error("applyFilter should not have been renamed")
	}
	if !names["applyFilter"] {
		t.Error("applyFilter should still be present with original name")
	}
}

func TestRenamePagerMethodsIgnoresNonPager(t *testing.T) {
	t.Parallel()

	src := `package gen

type customerBuilder struct{}

func (p *customerBuilder) toCursor(c *Node) Cursor { return Cursor{} }
func (p *customerBuilder) applyOrder(q *Query) *Query { return q }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	renamePagerMethods(file)

	names := funcNames(file)

	if names["ent_toCursor"] || names["ent_applyOrder"] {
		t.Error("methods on non-Pager receivers should not be renamed")
	}
}

func TestRenameWithOrderFuncs(t *testing.T) {
	t.Parallel()

	src := `package gen

func WithCustomerOrder(o *CustomerOrder) CustomerPaginateOption { return nil }
func WithItemOrder(o *ItemOrder) ItemPaginateOption { return nil }
func NewPaginator() *Paginator { return nil }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	renameWithOrderFuncs(file)

	names := funcNames(file)

	if !names["ent_WithCustomerOrder"] {
		t.Error("WithCustomerOrder should be renamed to ent_WithCustomerOrder")
	}
	if !names["ent_WithItemOrder"] {
		t.Error("WithItemOrder should be renamed to ent_WithItemOrder")
	}
	if !names["NewPaginator"] {
		t.Error("NewPaginator should not be renamed")
	}
	if names["ent_NewPaginator"] {
		t.Error("NewPaginator should not have been renamed")
	}
}

func TestAddOrderStructFields(t *testing.T) {
	t.Parallel()

	src := `package gen

type CustomerOrder struct {
	Direction OrderDirection
	Field     *CustomerOrderField
}

type SomeOtherStruct struct {
	Name string
}

type NotAnOrder struct {
	Direction OrderDirection
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	addOrderStructFields(file)

	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}

			fields := structFieldNames(st)

			switch ts.Name.Name {
			case "CustomerOrder":
				if !fields["JSONPath"] || !fields["JSONType"] {
					t.Error("CustomerOrder should have JSONPath and JSONType fields")
				}
				if len(st.Fields.List) != 4 {
					t.Errorf("CustomerOrder should have 4 fields, got %d", len(st.Fields.List))
				}
			case "SomeOtherStruct":
				if fields["JSONPath"] || fields["JSONType"] {
					t.Error("SomeOtherStruct should not have JSONB fields")
				}
			case "NotAnOrder":
				// Has Direction but not Field → should not be modified.
				if fields["JSONPath"] || fields["JSONType"] {
					t.Error("NotAnOrder should not have JSONB fields (missing Field)")
				}
			}
		}
	}
}

func TestAddOrderStructFieldsTags(t *testing.T) {
	t.Parallel()

	src := `package gen

type FooOrder struct {
	Direction OrderDirection
	Field     *FooOrderField
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	addOrderStructFields(file)

	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "FooOrder" {
				continue
			}
			st := ts.Type.(*ast.StructType)
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					continue
				}
				switch f.Names[0].Name {
				case "JSONPath":
					if f.Tag == nil || f.Tag.Value != "`json:\"jsonPath,omitempty\"`" {
						t.Errorf("JSONPath tag = %v, want `json:\"jsonPath,omitempty\"`", f.Tag)
					}
				case "JSONType":
					if f.Tag == nil || f.Tag.Value != "`json:\"jsonType,omitempty\"`" {
						t.Errorf("JSONType tag = %v, want `json:\"jsonType,omitempty\"`", f.Tag)
					}
				}
			}
		}
	}
}

func TestPostProcessPaginationMissingFile(t *testing.T) {
	t.Parallel()

	err := postProcessPagination(t.TempDir())
	if err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
}

func TestPostProcessPaginationRoundTrip(t *testing.T) {
	t.Parallel()

	src := `package gen

import "fmt"

type Cursor struct{}
type OrderDirection string
type CustomerOrderField struct{}
type CustomerQuery struct{}
type Querier interface{}
type Customer struct{}
type CustomerPaginateOption func(*customerPager) error

type CustomerOrder struct {
	Direction OrderDirection
	Field     *CustomerOrderField
}

type customerPager struct {
	order *CustomerOrder
}

func (p *customerPager) toCursor(c *Customer) Cursor       { return Cursor{} }
func (p *customerPager) applyCursors(q *CustomerQuery, after, before *Cursor) (*CustomerQuery, error) { return q, nil }
func (p *customerPager) applyOrder(q *CustomerQuery) *CustomerQuery { return q }
func (p *customerPager) orderExpr(q *CustomerQuery) Querier         { return nil }
func (p *customerPager) applyFilter(q *CustomerQuery) (*CustomerQuery, error) { return q, nil }

func WithCustomerOrder(o *CustomerOrder) CustomerPaginateOption { return nil }

func (p *customerPager) build() {
	_ = p.toCursor
	_ = p.applyOrder
	_ = fmt.Sprintf("keep import")
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "gql_pagination.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := postProcessPagination(dir); err != nil {
		t.Fatal(err)
	}

	result, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	output := string(result)

	// Verify output is valid Go.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, path, output, 0); err != nil {
		t.Fatalf("output is not valid Go: %v", err)
	}

	// Verify method and function renames.
	for _, name := range []string{"ent_toCursor", "ent_applyCursors", "ent_applyOrder", "ent_orderExpr", "ent_WithCustomerOrder"} {
		if !strings.Contains(output, name) {
			t.Errorf("expected %s in output", name)
		}
	}

	// applyFilter should NOT be renamed.
	if strings.Contains(output, "ent_applyFilter") {
		t.Error("applyFilter should not be renamed")
	}

	// Order struct fields should be added.
	if !strings.Contains(output, "JSONPath") || !strings.Contains(output, "JSONType") {
		t.Error("expected JSONPath and JSONType fields in CustomerOrder")
	}

	// Call sites in build() should NOT be renamed (AST only renames declarations).
	if strings.Contains(output, "p.ent_toCursor") {
		t.Error("call sites should not be renamed")
	}
}

// funcNames collects all FuncDecl names from the AST file.
func funcNames(file *ast.File) map[string]bool {
	m := make(map[string]bool)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		m[fn.Name.Name] = true
	}
	return m
}

// structFieldNames collects all named fields from a struct type.
func structFieldNames(st *ast.StructType) map[string]bool {
	m := make(map[string]bool)
	for _, f := range st.Fields.List {
		for _, id := range f.Names {
			m[id.Name] = true
		}
	}
	return m
}
