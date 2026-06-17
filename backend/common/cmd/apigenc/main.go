package main

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/urfave/cli/v2"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	_ "embed"
)

// ErrInternalClientNotFound is returned when the internal client_gen.go file cannot be found
var ErrInternalClientNotFound = fmt.Errorf("internal client_gen.go not found")

//go:embed templates/client.go.tmpl
var clientTemplate string

//go:embed templates/models.go.tmpl
var modelsTemplate string

const (
	logPrefix           = "apigen: "
	defaultOutputDir    = "./api"
	defaultPackageName  = "api"
	internalAPISuffix   = "/api/internal"
	packageAliasSuffix  = "api"
	generatedClientFile = "client_gen.go"
	generatedModelsFile = "models_gen.go"
	dryRunPrefix        = "[DRY-RUN] Would write: "
	generatedPrefix     = "Generated: "
)

type Method struct {
	Name               string
	Params             []Param
	Results            []Result
	HasContext         bool
	HasInterceptors    bool
	ContextType        string
	OrderType          string
	WhereType          string
	EntReturnType      string // Ent type from resolver signature (e.g., "*gen.AccessPolicyConnection")
	InternalReturnType string // Internal API type (e.g., "*GetInventoryCollections")
}

type Param struct {
	Name string
	Type string
}

type Result struct {
	Type string
}

type TypeAlias struct {
	Name string
}

type ConstAlias struct {
	Name string
}

type VarAlias struct {
	Name string
	Type string
}

type FuncAlias struct {
	Name       string
	Signature  string
	CallArgs   string
	HasResults bool
}

type TemplateData struct {
	Package           string
	InternalPackage   string
	InternalPkgPath   string
	ModuleBase        string // e.g., "github.com/pyck-ai/pyck/backend"
	ServiceName       string // e.g., "management"
	Methods           []Method
	TypeAliases       []TypeAlias
	ConstAliases      []ConstAlias
	VarAliases        []VarAlias
	FuncAliases       []FuncAlias
	InternalAPIAlias  string
	AdditionalImports []string // Additional imports needed (e.g., "github.com/google/uuid", "time")
	HasModelImport    bool     // Whether the internal client imports the model package
}

var (
	verbose bool
	dryRun  bool
)

func main() {
	// Configure log package to output without timestamps and with prefix
	log.SetFlags(0)
	log.SetPrefix(logPrefix)

	app := &cli.App{
		Name:  "apigen",
		Usage: "Generate API client code from internal API definitions",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "pkg",
				Aliases: []string{"p"},
				Usage:   "Import path of the internal API package (default: auto-detected from current directory)",
				Value:   "",
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   "Output directory for generated files",
				Value:   defaultOutputDir,
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Print the names of files as they are processed",
				Value:   false,
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Print commands that would be executed without modifying files",
				Value: false,
			},
		},
		Action: run,
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("%v", err)
	}
}

func run(c *cli.Context) error {
	ctx := c.Context
	verbose = c.Bool("verbose")
	dryRun = c.Bool("dry-run")

	pkgPath := c.String("pkg")
	outputDir := c.String("output")

	// If pkgPath is empty, determine it from the current directory
	if pkgPath == "" {
		pkg, err := getCurrentModulePath(ctx)
		if err != nil {
			return fmt.Errorf("failed to determine current module path: %w\nPlease specify --pkg flag explicitly", err)
		}
		pkgPath = pkg + internalAPISuffix
		logVerbosef("Auto-detected package path: %s", pkgPath)
	}

	logVerbosef("Using package: %s", pkgPath)
	logVerbosef("Output directory: %s", outputDir)
	if dryRun {
		logVerbosef("Dry-run mode enabled - no files will be written")
	}

	// Determine package alias from import path
	parts := strings.Split(pkgPath, "/")
	pkgAlias := parts[len(parts)-1] + packageAliasSuffix
	if verbose {
		log.Printf("Package alias: %s", pkgAlias)
	}

	// Extract service name and module base from package path
	// pkgPath format: "github.com/pyck-ai/pyck/backend/management/api/internal"
	// serviceName should be "management"
	// moduleBase should be "github.com/pyck-ai/pyck/backend"
	var serviceName, moduleBase string
	if len(parts) >= 3 {
		// Find "backend" in the path
		for i, part := range parts {
			if part == "backend" && i+1 < len(parts) {
				serviceName = parts[i+1]
				moduleBase = strings.Join(parts[:i+1], "/")
				break
			}
		}
	}
	if serviceName == "" || moduleBase == "" {
		return fmt.Errorf("%w: %s", ErrInvalidPackagePath, pkgPath)
	}
	logVerbosef("Service name: %s", serviceName)
	logVerbosef("Module base: %s", moduleBase)

	// Parse the internal API client interface
	methods, err := parseAPIClientInterface(ctx, pkgPath)
	if err != nil {
		return fmt.Errorf("failed to parse APIClient interface: %w", err)
	}
	if verbose {
		log.Printf("Found %d methods in APIClient interface", len(methods))
	}

	// Parse resolver signatures to get Ent return types
	if err := parseResolverReturnTypes(ctx, pkgPath, methods); err != nil {
		return fmt.Errorf("failed to parse resolver return types: %w", err)
	}

	// Parse internal API return types
	if err := parseInternalReturnTypes(ctx, pkgPath, methods); err != nil {
		return fmt.Errorf("failed to parse internal return types: %w", err)
	}

	// Validate that all methods have internal return types and log final mappings
	var missingTypes []string
	for _, method := range methods {
		if method.InternalReturnType == "" {
			missingTypes = append(missingTypes, method.Name)
		} else if verbose {
			log.Printf("Type mapping: %s -> %s (internal: %s)", method.Name, method.EntReturnType, method.InternalReturnType)
		}
	}
	if len(missingTypes) > 0 {
		return fmt.Errorf("%w (cannot determine internal return type): %v", ErrMissingEntReturnType, missingTypes)
	}

	// Parse model types from internal/api/models_gen.go
	typeAliases, constAliases, varAliases, funcAliases, err := parseModelTypes(ctx, pkgPath)
	if err != nil {
		log.Fatalf("failed to parse model types: %v", err)
	}
	if verbose {
		log.Printf("Found %d type aliases", len(typeAliases))
		log.Printf("Found %d const aliases", len(constAliases))
		log.Printf("Found %d var aliases", len(varAliases))
		log.Printf("Found %d func aliases", len(funcAliases))
	}

	// Detect additional imports needed based on parameter types
	additionalImports := detectRequiredImports(methods)

	// Detect if the internal client imports the model package
	hasModelImport, err := detectModelImport(ctx, pkgPath, moduleBase, serviceName)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDetectModelImport, err)
	}

	data := TemplateData{
		Package:           defaultPackageName,
		InternalPackage:   pkgAlias,
		InternalPkgPath:   pkgPath,
		ModuleBase:        moduleBase,
		ServiceName:       serviceName,
		Methods:           methods,
		TypeAliases:       typeAliases,
		ConstAliases:      constAliases,
		VarAliases:        varAliases,
		FuncAliases:       funcAliases,
		InternalAPIAlias:  pkgAlias,
		AdditionalImports: additionalImports,
		HasModelImport:    hasModelImport,
	}

	// Generate client_gen.go
	clientPath := filepath.Join(outputDir, generatedClientFile)
	if err := generateClient(data, outputDir); err != nil {
		log.Fatalf("failed to generate client: %v", err)
	}
	if dryRun {
		logDryRunf("%s", clientPath)
		logDryRunf("...")
		logDryRunf("type Client interface {")
		for _, method := range methods {
			logDryRunf("    %s", formatMethodSignature(method))
		}
		logDryRunf("}")
		logDryRunf("...")
	} else {
		logVerbosef(generatedPrefix+"%s", clientPath)
	}

	// Generate models_gen.go
	modelsPath := filepath.Join(outputDir, generatedModelsFile)
	if err := generateModels(data, outputDir); err != nil {
		log.Fatalf("failed to generate models: %v", err)
	}
	if dryRun {
		logDryRunf("%s", modelsPath)
		for _, alias := range typeAliases {
			logDryRunf("type %s = %s.%s", alias.Name, pkgAlias, alias.Name)
		}
		for _, alias := range constAliases {
			logDryRunf("const %s = %s.%s", alias.Name, pkgAlias, alias.Name)
		}
		for _, alias := range varAliases {
			logDryRunf("var %s = %s.%s", alias.Name, pkgAlias, alias.Name)
		}
	} else {
		logVerbosef(generatedPrefix+"%s", modelsPath)
	}

	// Silent success - go generators don't typically print success messages
	return nil
}

// logVerbosef logs a message only if verbose mode is enabled
func logVerbosef(format string, args ...interface{}) {
	if verbose {
		log.Printf(format, args...)
	}
}

// logDryRunf logs a dry-run message regardless of verbose settings
func logDryRunf(format string, args ...interface{}) {
	log.Printf(dryRunPrefix+format, args...)
}

// formatMethodSignature formats a Method into its Go signature string
func formatMethodSignature(m Method) string {
	// For methods with ContextType, use the new input object pattern
	if m.ContextType != "" {
		return fmt.Sprintf("%s(ctx context.Context, input %sInput, interceptors ...clientv2.RequestInterceptor) (*internalapi.%s, error)",
			m.Name, m.ContextType, m.Name)
	}

	// For methods without ContextType, use direct parameters
	params := make([]string, 0, len(m.Params)+1)
	params = append(params, "ctx context.Context")
	for _, p := range m.Params {
		params = append(params, fmt.Sprintf("%s %s", p.Name, p.Type))
	}

	// Build result list
	results := make([]string, 0, len(m.Results))
	for _, r := range m.Results {
		results = append(results, r.Type)
	}

	// Format signature
	resultStr := ""
	if len(results) == 1 {
		resultStr = " " + results[0]
	} else if len(results) > 1 {
		resultStr = " (" + strings.Join(results, ", ") + ")"
	}

	return fmt.Sprintf("%s(%s)%s", m.Name, strings.Join(params, ", "), resultStr)
}

// getCurrentModulePath returns the import path of the current directory
func getCurrentModulePath(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.ImportPath}}")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run 'go list -f {{.ImportPath}}': %w", err)
	}

	importPath := strings.TrimSpace(string(output))
	if importPath == "" {
		return "", ErrNoImportPath
	}

	return importPath, nil
}

func parseAPIClientInterface(ctx context.Context, pkgPath string) ([]Method, error) {
	// Use go list to find the directory for the package
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", pkgPath)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to locate package %s: %w", pkgPath, err)
	}

	pkgDir := strings.TrimSpace(string(output))
	clientFile := filepath.Join(pkgDir, "client_gen.go")

	logVerbosef("Processing: %s", clientFile)
	if verbose {
		log.Printf("Parsing client file: %s", clientFile)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, clientFile, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var methods []Method

	ast.Inspect(file, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != "APIClient" {
			return true
		}

		ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}

		for _, method := range ifaceType.Methods.List {
			funcType, ok := method.Type.(*ast.FuncType)
			if !ok {
				continue
			}

			m := Method{
				Name: method.Names[0].Name,
			}

			// Parse parameters
			if funcType.Params != nil {
				for _, field := range funcType.Params.List {
					typeStr := exprToString(field.Type)

					// Check for context.Context - skip adding to Params
					if typeStr == "context.Context" {
						m.HasContext = true
						continue
					}

					// Check for variadic interceptors - skip adding to Params
					if strings.Contains(typeStr, "...clientv2.RequestInterceptor") {
						m.HasInterceptors = true
						continue
					}

					for _, name := range field.Names {
						m.Params = append(m.Params, Param{
							Name: name.Name,
							Type: typeStr,
						})
					}
				}
			}

			// Parse results
			if funcType.Results != nil {
				for _, field := range funcType.Results.List {
					m.Results = append(m.Results, Result{
						Type: exprToString(field.Type),
					})
				}
			}

			// Detect query methods that return connection types
			if len(m.Results) == 2 && m.Results[1].Type == "error" {
				resultType := m.Results[0].Type
				if strings.HasPrefix(resultType, "*") {
					// This is a query method - determine order and where types
					m.ContextType = strings.ToLower(m.Name[:1]) + m.Name[1:] + "Context"

					// Try to infer types from parameters
					for _, p := range m.Params {
						if strings.HasSuffix(p.Type, "Order") {
							m.OrderType = strings.TrimPrefix(p.Type, "*")
						} else if strings.HasSuffix(p.Type, "WhereInput") {
							m.WhereType = strings.TrimPrefix(p.Type, "*")
						}
					}
				}
			}

			methods = append(methods, m)
		}

		return false
	})

	return methods, nil
}

func parseModelTypes(ctx context.Context, pkgPath string) ([]TypeAlias, []ConstAlias, []VarAlias, []FuncAlias, error) {
	// Use go list to find the directory for the package
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", pkgPath)
	output, err := cmd.Output()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to locate package %s: %w", pkgPath, err)
	}

	pkgDir := strings.TrimSpace(string(output))

	var typeAliases []TypeAlias
	var constAliases []ConstAlias
	var varAliases []VarAlias
	var funcAliases []FuncAlias
	seenTypes := make(map[string]bool)
	seenConsts := make(map[string]bool)
	seenVars := make(map[string]bool)
	seenFuncs := make(map[string]bool)

	filesToProcess := []string{"models_gen.go", "client_gen.go"}

	for _, fileName := range filesToProcess {
		filePath := filepath.Join(pkgDir, fileName)

		// Check if file exists
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			if verbose {
				log.Printf(logPrefix+"File not found, skipping: %s", filePath)
			}
			continue
		}

		logVerbosef("Processing: %s", filePath)
		logVerbosef("Parsing file: %s", filePath)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to parse %s: %w", fileName, err)
		}

		isClientGen := fileName == "client_gen.go"

		ast.Inspect(file, func(n ast.Node) bool {
			switch decl := n.(type) {
			case *ast.GenDecl:
				switch decl.Tok {
				case token.TYPE:
					for _, spec := range decl.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						name := typeSpec.Name.Name

						// Skip unexported types
						if !ast.IsExported(name) {
							continue
						}

						// For client_gen.go, skip Client and APIClient types
						if isClientGen && (name == "Client" || name == "APIClient") {
							continue
						}

						// Skip types that are already defined
						if seenTypes[name] {
							continue
						}

						seenTypes[name] = true
						typeAliases = append(typeAliases, TypeAlias{Name: name})
					}
				case token.CONST:
					// Skip consts for client_gen.go
					if isClientGen {
						break
					}
					for _, spec := range decl.Specs {
						valueSpec, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, name := range valueSpec.Names {
							// Skip unexported constants
							if !ast.IsExported(name.Name) {
								continue
							}

							// Skip constants that are already defined
							if seenConsts[name.Name] {
								continue
							}

							seenConsts[name.Name] = true
							constAliases = append(constAliases, ConstAlias{Name: name.Name})
						}
					}
				case token.VAR:
					// Skip vars for client_gen.go
					if isClientGen {
						break
					}
					for _, spec := range decl.Specs {
						valueSpec, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, name := range valueSpec.Names {
							// Skip unexported variables
							if !ast.IsExported(name.Name) {
								continue
							}

							// Skip variables that are already defined
							if seenVars[name.Name] {
								continue
							}

							// Get the variable type (may be nil if inferred)
							varType := ""
							if valueSpec.Type != nil {
								varType = exprToString(valueSpec.Type)
							} else if len(valueSpec.Values) > 0 {
								// Type is inferred from value, try to extract from value
								varType = inferTypeFromValue(valueSpec.Values[0])
							}

							seenVars[name.Name] = true
							varAliases = append(varAliases, VarAlias{Name: name.Name, Type: varType})
						}
					}
				}
			case *ast.FuncDecl:
				// Skip funcs for client_gen.go
				if isClientGen {
					return true
				}
				// Skip methods (only extract top-level functions)
				if decl.Recv != nil || decl.Name == nil {
					return true
				}

				name := decl.Name.Name

				// Skip unexported functions
				if !ast.IsExported(name) {
					return true
				}

				// Skip functions that are already defined
				if seenFuncs[name] {
					return true
				}

				// Build function signature
				var params []string
				var callArgs []string
				if decl.Type.Params != nil {
					for _, field := range decl.Type.Params.List {
						typeStr := exprToString(field.Type)
						for _, paramName := range field.Names {
							params = append(params, fmt.Sprintf("%s %s", paramName.Name, typeStr))
							callArgs = append(callArgs, paramName.Name)
						}
					}
				}

				var results []string
				if decl.Type.Results != nil {
					for _, field := range decl.Type.Results.List {
						results = append(results, exprToString(field.Type))
					}
				}

				// Format signature
				resultStr := ""
				if len(results) == 1 {
					resultStr = " " + results[0]
				} else if len(results) > 1 {
					resultStr = " (" + strings.Join(results, ", ") + ")"
				}

				signature := fmt.Sprintf("(%s)%s", strings.Join(params, ", "), resultStr)

				seenFuncs[name] = true
				funcAliases = append(funcAliases, FuncAlias{
					Name:       name,
					Signature:  signature,
					CallArgs:   strings.Join(callArgs, ", "),
					HasResults: len(results) > 0,
				})
				if verbose {
					log.Printf(logPrefix+"  Found func: %s%s", name, signature)
				}
			}
			return true
		})
	}

	return typeAliases, constAliases, varAliases, funcAliases, nil
}

// parseResolverReturnTypes parses resolver files to extract Ent return types
func parseResolverReturnTypes(ctx context.Context, pkgPath string, methods []Method) error {
	// Convert internal API path to service path
	// e.g., "github.com/pyck-ai/pyck/backend/management/api/internal" -> "github.com/pyck-ai/pyck/backend/management"
	servicePath := strings.TrimSuffix(pkgPath, "/api/internal")

	// Use go list to find the directory for the service
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", servicePath)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to locate service package %s: %w", servicePath, err)
	}

	serviceDir := strings.TrimSpace(string(output))
	resolversDir := filepath.Join(serviceDir, "resolvers")

	// Check if resolvers directory exists
	if _, err := os.Stat(resolversDir); os.IsNotExist(err) {
		return nil // Not an error, just no resolvers to parse
	}

	// Create a map of method names to their index in the methods slice
	methodMap := make(map[string]*Method)
	for i := range methods {
		methodMap[methods[i].Name] = &methods[i]
	}

	// Find all .resolvers.go files
	resolverFiles, err := filepath.Glob(filepath.Join(resolversDir, "*.resolvers.go"))
	if err != nil {
		return fmt.Errorf("failed to glob resolver files: %w", err)
	}

	resolverCount := 0
	for _, resolverFile := range resolverFiles {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, resolverFile, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		// Look for queryResolver and mutationResolver methods
		ast.Inspect(file, func(n ast.Node) bool {
			funcDecl, ok := n.(*ast.FuncDecl)
			if !ok || funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
				return true
			}

			// Check if this is a queryResolver or mutationResolver method
			recvType := exprToString(funcDecl.Recv.List[0].Type)
			if !strings.Contains(recvType, "queryResolver") && !strings.Contains(recvType, "mutationResolver") {
				return true
			}

			methodName := funcDecl.Name.Name

			// Try exact match first
			method, exists := methodMap[methodName]

			// If not found, try with "Get" prefix (client methods are prefixed with "Get")
			if !exists {
				clientMethodName := "Get" + methodName
				method, exists = methodMap[clientMethodName]
			}

			if !exists {
				return true
			}

			// Extract the return type (first non-error return value)
			if funcDecl.Type.Results != nil && len(funcDecl.Type.Results.List) >= 2 {
				returnType := exprToString(funcDecl.Type.Results.List[0].Type)
				// Normalize the type: replace "ent." with "gen." since resolvers use ent package alias
				// but the actual types are in the gen package
				returnType = strings.Replace(returnType, "*ent.", "*gen.", 1)
				returnType = strings.Replace(returnType, "[]ent.", "[]gen.", 1)

				method.EntReturnType = returnType
				resolverCount++
			}

			return true
		})
	}

	if verbose && resolverCount > 0 {
		log.Printf(logPrefix+"Mapped %d resolver return types", resolverCount)
	}

	return nil
}

// parseInternalReturnTypes parses the internal API client to extract return types
func parseInternalReturnTypes(ctx context.Context, pkgPath string, methods []Method) error {
	// Use go list to find the directory for the internal package
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", pkgPath)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to locate internal package %s: %w", pkgPath, err)
	}

	pkgDir := strings.TrimSpace(string(output))
	clientFile := filepath.Join(pkgDir, "client_gen.go")

	// Check if file exists
	if _, err := os.Stat(clientFile); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrInternalClientNotFound, clientFile)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, clientFile, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("failed to parse internal client_gen.go: %w", err)
	}

	// Create a map of method names to their index
	methodMap := make(map[string]*Method)
	for i := range methods {
		methodMap[methods[i].Name] = &methods[i]
	}

	// Look for the APIClient interface
	ast.Inspect(file, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != "APIClient" {
			return true
		}

		ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}

		for _, methodDecl := range ifaceType.Methods.List {
			funcType, ok := methodDecl.Type.(*ast.FuncType)
			if !ok || len(methodDecl.Names) == 0 {
				continue
			}

			methodName := methodDecl.Names[0].Name
			method, exists := methodMap[methodName]
			if !exists {
				continue
			}

			// Extract the first return type (non-error)
			if funcType.Results != nil && len(funcType.Results.List) >= 2 {
				returnType := exprToString(funcType.Results.List[0].Type)
				method.InternalReturnType = returnType
			}
		}

		return false
	})

	return nil
}

func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.ArrayType:
		return "[]" + exprToString(e.Elt)
	case *ast.Ellipsis:
		return "..." + exprToString(e.Elt)
	case *ast.InterfaceType:
		return "any"
	default:
		return fmt.Sprintf("%T", e)
	}
}

// detectModelImport checks if the internal client imports the model package
func detectModelImport(ctx context.Context, pkgPath string, moduleBase string, serviceName string) (bool, error) {
	// Use go list to find the directory for the internal package
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", pkgPath)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to locate internal package %s: %w", pkgPath, err)
	}

	pkgDir := strings.TrimSpace(string(output))
	clientFile := filepath.Join(pkgDir, "client_gen.go")

	// Check if file exists
	if _, err := os.Stat(clientFile); os.IsNotExist(err) {
		return false, fmt.Errorf("%w: %s", ErrInternalClientNotFound, clientFile)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, clientFile, nil, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("failed to parse internal client_gen.go: %w", err)
	}

	// Expected model package import path
	modelImportPath := fmt.Sprintf("%s/%s/model", moduleBase, serviceName)

	// Check if the model package is imported
	for _, imp := range file.Imports {
		if imp.Path != nil {
			importPath := strings.Trim(imp.Path.Value, "\"")
			if importPath == modelImportPath {
				return true, nil
			}
		}
	}

	return false, nil
}

// detectRequiredImports scans all method parameters and return types to determine
// which additional imports are needed beyond the standard ones.
//
// Only scan types that actually appear in the generated wrapper file
// (params land in {Method}Args structs; InternalReturnType lands in
// the Client interface). EntReturnType is the resolver's return type,
// which never appears in the wrapper, so scanning it adds spurious
// imports (e.g. uuid for a query that returns []uuid.UUID — the wrapper
// just returns the opaque internal *Get... struct).
func detectRequiredImports(methods []Method) []string {
	importMap := make(map[string]bool)

	for _, method := range methods {
		for _, param := range method.Params {
			checkTypeForImports(param.Type, importMap)
		}

		if method.InternalReturnType != "" {
			checkTypeForImports(method.InternalReturnType, importMap)
		}
	}

	imports := make([]string, 0, len(importMap))
	for imp := range importMap {
		imports = append(imports, imp)
	}

	return imports
}

// checkTypeForImports checks a type string and adds required imports to the map
func checkTypeForImports(typeStr string, importMap map[string]bool) {
	// Check for uuid.UUID
	if strings.Contains(typeStr, "uuid.UUID") {
		importMap["github.com/google/uuid"] = true
	}

	// Check for time.Time or time.Duration
	if strings.Contains(typeStr, "time.Time") || strings.Contains(typeStr, "time.Duration") {
		importMap["time"] = true
	}
}

// inferTypeFromValue attempts to infer the type from a variable's initial value
func inferTypeFromValue(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		// e.g., []SomeType{...} or SomeType{...}
		if e.Type != nil {
			return exprToString(e.Type)
		}
	case *ast.CallExpr:
		// e.g., make([]SomeType, 0)
		if fun, ok := e.Fun.(*ast.Ident); ok && fun.Name == "make" {
			if len(e.Args) > 0 {
				return exprToString(e.Args[0])
			}
		}
	}
	return ""
}

func generateClient(data TemplateData, outputDir string) error {
	funcMap := template.FuncMap{
		"title": cases.Title(language.Und, cases.NoLower).String,
	}
	tmpl, err := template.New("client").Funcs(funcMap).Parse(clientTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	// Format the generated code
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		// Write unformatted code for debugging
		return fmt.Errorf("failed to format generated code: %w\n%s", err, buf.String())
	}

	outputPath := filepath.Join(outputDir, "client_gen.go")

	// Skip writing in dry-run mode
	if dryRun {
		if verbose {
			log.Printf(logPrefix+"Would write %d bytes to %s", len(formatted), outputPath)
		}
		return nil
	}

	return os.WriteFile(outputPath, formatted, 0o600)
}

func generateModels(data TemplateData, outputDir string) error {
	tmpl, err := template.New("models").Parse(modelsTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	// Format the generated code
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format generated code: %w\n%s", err, buf.String())
	}

	outputPath := filepath.Join(outputDir, "models_gen.go")

	// Skip writing in dry-run mode
	if dryRun {
		if verbose {
			log.Printf(logPrefix+"Would write %d bytes to %s", len(formatted), outputPath)
		}
		return nil
	}

	return os.WriteFile(outputPath, formatted, 0o600)
}
