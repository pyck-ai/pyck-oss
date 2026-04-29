// Package internal contains importgen implementation details.
package internal

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/cmd/importgen/types"
)

const apiClientInterface = "APIClient"

// ParseClientMethods parses the API client interface from client_gen.go and
// returns a map of method name to ClientMethod.
func ParseClientMethods(clientPath string) (map[string]types.ClientMethod, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, clientPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", clientPath, err)
	}

	methods := make(map[string]types.ClientMethod)

	ast.Inspect(file, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != apiClientInterface {
			return true
		}

		ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}

		for _, m := range ifaceType.Methods.List {
			funcType, ok := m.Type.(*ast.FuncType)
			if !ok || len(m.Names) == 0 {
				continue
			}

			cm := types.ClientMethod{Name: m.Names[0].Name}

			if funcType.Params != nil {
				for _, field := range funcType.Params.List {
					typeStr := ExprToString(field.Type)
					if typeStr == "context.Context" || strings.Contains(typeStr, "RequestInterceptor") {
						continue
					}
					for _, name := range field.Names {
						cm.Params = append(cm.Params, types.MethodParam{
							Name: name.Name,
							Type: typeStr,
						})
					}
				}
			}

			methods[cm.Name] = cm
		}

		return false
	})

	return methods, nil
}

// DetectAccessorChain parses the internal client_gen.go to determine the
// response accessor chain for a mutation method.
//
// For a method "CreateLocation", it finds the inner struct
// CreateLocation_CreateLocation and inspects its fields:
//   - Has an ID field → entity struct (1-level): "GetCreateLocation()"
//   - No ID field → wrapper struct, find entity field (2-level): "GetCreateLocation().GetLocation()"
//
// The entity field inside a wrapper is the first non-slice field whose type is
// a struct defined in the same file (covers both pointer and value types,
// handling nullable and non-nullable GraphQL fields).
func DetectAccessorChain(clientPath, methodName string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, clientPath, nil, 0)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", clientPath, err)
	}

	firstAccessor := "Get" + methodName + "()"
	innerStructName := methodName + "_" + methodName

	innerStruct := findStructType(file, innerStructName)
	if innerStruct == nil {
		return firstAccessor, nil
	}

	// Entity structs have an ID field directly → 1-level accessor.
	for _, field := range innerStruct.Fields.List {
		for _, name := range field.Names {
			if name.Name == "ID" {
				return firstAccessor, nil
			}
		}
	}

	// Wrapper struct — find the entity field. It is the first exported field
	// that references a struct type (not a slice), regardless of whether it
	// is a pointer (*T for nullable) or value type (T for non-nullable).
	structNames := collectStructNames(file)
	for _, field := range innerStruct.Fields.List {
		if len(field.Names) == 0 {
			continue
		}
		typeName := scalarTypeName(field.Type)
		if typeName != "" && structNames[typeName] {
			return firstAccessor + ".Get" + field.Names[0].Name + "()", nil
		}
	}

	return firstAccessor, nil
}

// scalarTypeName returns the type name for a field that is either an ident (T)
// or a pointer (*T). It returns "" for slices, maps, and other compound types.
func scalarTypeName(expr ast.Expr) string {
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

// collectStructNames returns a set of all struct type names declared in the file.
func collectStructNames(file *ast.File) map[string]bool {
	names := make(map[string]bool)
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := typeSpec.Type.(*ast.StructType); ok {
				names[typeSpec.Name.Name] = true
			}
		}
	}
	return names
}

func findStructType(file *ast.File, name string) *ast.StructType {
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if typeSpec.Name.Name == name {
				if structType, ok := typeSpec.Type.(*ast.StructType); ok {
					return structType
				}
			}
		}
	}
	return nil
}

// ExprToString converts a Go AST expression to its string representation.
func ExprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return ExprToString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + ExprToString(e.X)
	case *ast.ArrayType:
		return "[]" + ExprToString(e.Elt)
	case *ast.Ellipsis:
		return "..." + ExprToString(e.Elt)
	case *ast.InterfaceType:
		return "any"
	default:
		return fmt.Sprintf("%T", e)
	}
}
