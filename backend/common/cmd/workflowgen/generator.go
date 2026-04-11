package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

// Generator encapsulates the workflow import generation logic.
// It uses dependency injection for the file system to enable in-memory testing.
type Generator struct {
	fs           FileSystem
	modulePath   string
	mainTemplate string
	verbose      bool
	dryRun       bool
}

// NewGenerator creates a new Generator with the given file system and module path.
func NewGenerator(fs FileSystem, modulePath string, template string) *Generator {
	return &Generator{
		fs:           fs,
		modulePath:   modulePath,
		mainTemplate: template,
		verbose:      verbose, // Use global verbose flag
		dryRun:       false,
	}
}

// FindPackagesWithInit finds all packages containing init() functions in the given search path.
// Supports both recursive (path/...) and non-recursive (path) searches.
func (g *Generator) FindPackagesWithInit(searchPath string) ([]string, error) {
	var imports []string
	packagesFound := make(map[string]bool)

	// Check if recursive search is requested
	recursive := strings.HasSuffix(searchPath, "/...")
	basePath := searchPath
	if recursive {
		basePath = strings.TrimSuffix(searchPath, "/...")
		if basePath == "" {
			basePath = "."
		}
	}

	if recursive {
		return imports, g.processRecursive(basePath, packagesFound, &imports)
	}

	return imports, g.processNonRecursive(basePath, packagesFound, &imports)
}

// UpdateMainFile updates the imports in main.go or creates it if it doesn't exist.
// Returns (added, removed, error).
func (g *Generator) UpdateMainFile(newImports []string) ([]string, []string, error) {
	const mainFilePath = "main.go"

	// Read the file content
	content, err := g.fs.ReadFile(mainFilePath)
	if err != nil {
		// If file doesn't exist, create a new one
		if os.IsNotExist(err) {
			err := g.createMainFile(newImports)
			return newImports, nil, err
		}
		return nil, nil, fmt.Errorf("failed to read main.go: %w", err)
	}

	fset := token.NewFileSet()

	// Parse existing main.go
	file, err := parser.ParseFile(fset, mainFilePath, content, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse main.go: %w", err)
	}

	// Track existing imports by category
	var existingUnderscoreImports []string
	existingImportSpecs := make(map[string]*ast.ImportSpec) // non-underscore imports

	// Find the import declaration
	var importDecl *ast.GenDecl
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if ok && genDecl.Tok == token.IMPORT {
			importDecl = genDecl
			break
		}
	}

	// Track if we created a new import declaration
	createdNewImport := false

	// If no import declaration exists, create one
	if importDecl == nil {
		createdNewImport = true
		importDecl = &ast.GenDecl{
			Tok:    token.IMPORT,
			Lparen: 1, // Indicate this is a grouped import
		}
		// Insert after package declaration
		if len(file.Decls) > 0 {
			file.Decls = append([]ast.Decl{file.Decls[0], importDecl}, file.Decls[1:]...)
		} else {
			file.Decls = []ast.Decl{importDecl}
		}
	}

	// Extract existing imports
	for _, spec := range importDecl.Specs {
		importSpec, ok := spec.(*ast.ImportSpec)
		if !ok {
			continue
		}

		importPath := strings.Trim(importSpec.Path.Value, `"`)

		// Check if this is an underscore import
		if importSpec.Name != nil && importSpec.Name.Name == "_" {
			existingUnderscoreImports = append(existingUnderscoreImports, importPath)
		} else {
			// Keep non-underscore imports as-is
			existingImportSpecs[importPath] = importSpec
		}
	}

	// Calculate added and removed imports
	existingSet := make(map[string]bool)
	for _, imp := range existingUnderscoreImports {
		existingSet[imp] = true
	}

	newSet := make(map[string]bool)
	for _, imp := range newImports {
		newSet[imp] = true
	}

	var added []string
	var removed []string

	// Find added imports
	for _, imp := range newImports {
		if !existingSet[imp] {
			added = append(added, imp)
		}
	}

	// Find removed imports
	for _, imp := range existingUnderscoreImports {
		if !newSet[imp] {
			removed = append(removed, imp)
		}
	}

	// Rebuild imports manually to ensure proper blank line separation
	// First, collect all non-underscore import lines
	nonUnderscoreLines := make([]string, 0, len(existingImportSpecs))
	for _, spec := range existingImportSpecs {
		importPath := strings.Trim(spec.Path.Value, `"`)

		// Build import line with name if present
		var importLine string
		if spec.Name != nil && spec.Name.Name != "" {
			importLine = fmt.Sprintf("%s %q", spec.Name.Name, importPath)
		} else {
			importLine = fmt.Sprintf("%q", importPath)
		}
		nonUnderscoreLines = append(nonUnderscoreLines, importLine)
	}

	// Sort underscore imports for consistent output
	sort.Strings(newImports)

	// Build the complete import block as a string
	var importBlock strings.Builder
	importBlock.WriteString("import (\n")

	// Add non-underscore imports
	for _, line := range nonUnderscoreLines {
		importBlock.WriteString("\t")
		importBlock.WriteString(line)
		importBlock.WriteString("\n")
	}

	// Add blank line before underscore imports if we have both types
	if len(nonUnderscoreLines) > 0 && len(newImports) > 0 {
		importBlock.WriteString("\n")
	}

	// Add underscore imports
	for _, imp := range newImports {
		importBlock.WriteString("\t_ ")
		importBlock.WriteString(fmt.Sprintf("%q", imp))
		importBlock.WriteString("\n")
	}

	importBlock.WriteString(")\n")

	// Now reconstruct the entire file with the new import block
	var buf bytes.Buffer

	// Handle newly created import vs existing import differently
	if createdNewImport {
		// For newly created imports, find the package declaration end
		// and insert the import block after it
		packageEnd := fset.Position(file.Name.End()).Offset

		// Write everything up to and including the package declaration
		buf.Write(content[:packageEnd])
		buf.WriteString("\n\n")

		// Write the import block
		buf.WriteString(importBlock.String())

		// Write the rest of the file
		buf.Write(content[packageEnd:])
	} else {
		// For existing imports, replace the import declaration
		importStart := fset.Position(importDecl.Pos()).Offset
		importEnd := fset.Position(importDecl.End()).Offset

		// Write everything before the import declaration
		buf.Write(content[:importStart])

		// Write the import block
		buf.WriteString(importBlock.String())

		// Write everything after the import declaration
		buf.Write(content[importEnd:])
	}

	// Run gofmt on the output to ensure proper formatting
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		// If formatting fails, use unformatted output
		formatted = buf.Bytes()
	}

	// Handle dry-run mode
	if g.dryRun {
		fmt.Println("=== Generated main.go (dry-run mode) ===")
		fmt.Println(string(formatted))
		fmt.Println("=== End of generated output ===")
		return added, removed, nil
	}

	if err := g.fs.WriteFile(mainFilePath, formatted, 0o600); err != nil {
		return nil, nil, fmt.Errorf("failed to write main.go: %w", err)
	}

	return added, removed, nil
}

// FileHasInit checks if a Go file contains an init() function.
// This is exported to allow testing.
func (g *Generator) FileHasInit(content []byte) (bool, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", content, 0)
	if err != nil {
		return false, err
	}

	hasInit := false
	ast.Inspect(node, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok {
			if fn.Name.Name == "init" && fn.Recv == nil {
				hasInit = true
				return false
			}
		}
		return true
	})

	return hasInit, nil
}

// processRecursive handles recursive directory walking for finding init() functions
func (g *Generator) processRecursive(basePath string, packagesFound map[string]bool, imports *[]string) error {
	return g.fs.Walk(basePath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and non-Go files
		if info.IsDir() || !strings.HasSuffix(filePath, ".go") {
			return nil
		}

		// Skip test files
		if strings.HasSuffix(filePath, "_test.go") {
			return nil
		}

		return g.processFileForInit(filePath, packagesFound, imports)
	})
}

// processNonRecursive handles non-recursive directory scanning for finding init() functions
func (g *Generator) processNonRecursive(basePath string, packagesFound map[string]bool, imports *[]string) error {
	entries, err := g.fs.ReadDir(basePath)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", basePath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()
		if !strings.HasSuffix(fileName, ".go") || strings.HasSuffix(fileName, "_test.go") {
			continue
		}

		filePath := g.fs.Join(basePath, fileName)
		if err := g.processNonRecursiveFile(filePath, basePath, packagesFound, imports); err != nil {
			return err
		}
	}

	return nil
}

// processNonRecursiveFile checks a single file in non-recursive mode
func (g *Generator) processNonRecursiveFile(filePath, basePath string, packagesFound map[string]bool, imports *[]string) error {
	content, err := g.fs.ReadFile(filePath)
	if err != nil {
		return err
	}

	hasInit, err := g.FileHasInit(content)
	if err != nil {
		if g.verbose {
			fmt.Fprintf(os.Stderr, "Warning: error parsing %s: %v\n", filePath, err)
		}
		return nil
	}

	if !hasInit {
		return nil
	}

	if g.verbose {
		fmt.Printf("Found init() in: %s\n", filePath)
	}

	// Get package import path
	relPath, err := g.fs.Rel(".", basePath)
	if err != nil {
		return err
	}

	// Convert file path to import path
	importPath := strings.ReplaceAll(g.fs.Join(g.modulePath, relPath), "\\", "/")

	// Add to imports if not already added
	if !packagesFound[importPath] {
		packagesFound[importPath] = true
		*imports = append(*imports, importPath)
	}

	return nil
}

// processFileForInit checks if a file has init() and adds its package to imports
func (g *Generator) processFileForInit(filePath string, packagesFound map[string]bool, imports *[]string) error {
	content, err := g.fs.ReadFile(filePath)
	if err != nil {
		return err
	}

	hasInit, err := g.FileHasInit(content)
	if err != nil {
		if g.verbose {
			fmt.Fprintf(os.Stderr, "Warning: error parsing %s: %v\n", filePath, err)
		}
		return nil
	}

	if !hasInit {
		return nil
	}

	if g.verbose {
		fmt.Printf("Found init() in: %s\n", filePath)
	}

	// Get package import path
	pkgDir := g.fs.Dir(filePath)
	relPath, err := g.fs.Rel(".", pkgDir)
	if err != nil {
		return err
	}

	// Convert file path to import path
	importPath := strings.ReplaceAll(g.fs.Join(g.modulePath, relPath), "\\", "/")

	// Add to imports if not already added
	if !packagesFound[importPath] {
		packagesFound[importPath] = true
		*imports = append(*imports, importPath)
	}

	return nil
}

// createMainFile creates a new main.go file from template with the given imports
func (g *Generator) createMainFile(imports []string) error {
	// Write template as main.go
	if err := g.fs.WriteFile("main.go", []byte(g.mainTemplate), 0o600); err != nil {
		return fmt.Errorf("failed to create main.go from template: %w", err)
	}

	// If there are imports to add, patch them in
	if len(imports) > 0 {
		_, _, err := g.UpdateMainFile(imports)
		return err
	}

	return nil
}

// Run executes the full generation process for the given search paths.
// Returns (added, removed, error).
func (g *Generator) Run(searchPaths []string) ([]string, []string, error) {
	// Find all packages with init() methods from all specified paths
	var allImports []string
	importsSet := make(map[string]bool)

	for _, path := range searchPaths {
		imports, err := g.FindPackagesWithInit(path)
		if err != nil {
			return nil, nil, fmt.Errorf("error finding packages in %s: %w", path, err)
		}

		// Add to set to avoid duplicates
		for _, imp := range imports {
			if !importsSet[imp] {
				importsSet[imp] = true
				allImports = append(allImports, imp)
			}
		}
	}

	// Sort imports for consistent output
	sort.Strings(allImports)

	if g.verbose {
		fmt.Printf("Found %d package(s) with init() functions:\n", len(allImports))
		for _, imp := range allImports {
			fmt.Printf("  - %s\n", imp)
		}
		fmt.Println()
	}

	// Update or create main.go
	added, removed, err := g.UpdateMainFile(allImports)
	if err != nil {
		return nil, nil, fmt.Errorf("error updating main.go: %w", err)
	}

	if g.verbose {
		if len(added) > 0 {
			fmt.Printf("Added %d import(s):\n", len(added))
			for _, imp := range added {
				fmt.Printf("  + %s\n", imp)
			}
		}
		if len(removed) > 0 {
			fmt.Printf("Removed %d import(s):\n", len(removed))
			for _, imp := range removed {
				fmt.Printf("  - %s\n", imp)
			}
		}
		if len(added) == 0 && len(removed) == 0 {
			fmt.Println("No changes needed")
		} else {
			fmt.Println("\nSuccessfully updated main.go")
		}
	}

	return added, removed, nil
}
