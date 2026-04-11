package instructions

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/types"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/pyck-ai/pyck/backend/common/commands"
	"github.com/spf13/cobra"
	"golang.org/x/tools/go/packages"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

const (
	executableFileNameFlag = "executable-file"
	projectRootFlag        = "project-root"
	aliasFallback          = "pkg"
)

var projectRoot string

var (
	mainTemplates    = template.Must(template.ParseFS(templatesFS, "templates/*.tmpl"))
	mainTemplateName = "main.go.tmpl"
)

// Global state for managing unique package aliases
var (
	// Maps a full package import path to its assigned unique alias (e.g., "github.com/foo/bar" -> "bar1").
	packageAlias = make(map[string]string)
	// Counts the number of times a base alias (e.g., "utils") has been used,
	// allowing for unique numbering (e.g., "utils1", "utils2").
	aliasCounts = make(map[string]int)
)

type mainTemplateData struct {
	Imports    []string
	Workflows  []string
	Activities []string
}

// nolint:gochecknoinits // init required for cobra command setup
func init() {
	projectCmd.PersistentFlags().StringVar(&projectRoot, projectRootFlag, "", "Path to the customer project root (defaults to current directory)")

	buildProjectCmd.Flags().String(executableFileNameFlag, "worker", "Executable file name, example worker")
	runProjectCmd.Flags().String(executableFileNameFlag, "worker", "Executable file name, example worker")

	projectCmd.AddCommand(buildProjectCmd)
	projectCmd.AddCommand(generateProjectCmd)
	projectCmd.AddCommand(runProjectCmd)

	rootCmd.AddCommand(projectCmd)
}

type ImportSpec struct {
	Alias string
	Path  string
}

type WorkflowDescriptorMeta struct {
	Import     ImportSpec
	Identifier string
	HasFn      bool
	HasSignals bool
	Activities []ActivityDescriptorMeta
}

type ActivityDescriptorMeta struct {
	Import     ImportSpec
	Identifier string
}

type WorkflowMetadataResult struct {
	Workflows             []WorkflowDescriptorMeta
	Activities            []ActivityDescriptorMeta
	WorkflowsdkImportPath string
}

var (
	errWorkflowsdkDetection     = errors.New("workflowsdk import path detection failed")
	errWorkflowsdkMetadataUnset = errors.New("workflowsdk import path is not set in metadata")
	errNoWorkflowsDiscovered    = errors.New("no workflows discovered")
	errDuplicateWorkflows       = errors.New("duplicate workflows found")
	errDuplicateActivities      = errors.New("duplicate activities found")
	errModulePathNotFound       = errors.New("module path not found")
)

// resolveProjectRoot resolves the absolute path to the project root directory.
// It uses the projectRoot flag if set, otherwise defaults to the current working directory.
func resolveProjectRoot() (string, error) {
	if projectRoot != "" {
		abs, err := filepath.Abs(projectRoot)
		if err != nil {
			return "", fmt.Errorf("failed to resolve project root %q: %w", projectRoot, err)
		}
		return abs, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to determine project root: %w", err)
	}
	return cwd, nil
}

// sanitizeAlias converts a name (like a package path segment or identifier) into a
// safe, lowercase, and valid Go package alias.
func sanitizeAlias(name string) string {
	if name == "" {
		return aliasFallback
	}
	b := strings.Builder{}
	for _, r := range name {
		switch {
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsDigit(r):
			if b.Len() == 0 {
				// Prepend fallback if the first non-skipped char is a digit
				b.WriteString(aliasFallback)
			}
			b.WriteRune(r)
		default:
			// Skip separators such as '_' or '-' entirely
		}
	}
	alias := b.String()
	if alias == "" || !unicode.IsLetter(rune(alias[0])) {
		// Ensure it starts with a letter
		alias = aliasFallback + alias
	}
	return alias
}

// trimNumericPrefix removes leading digits and underscores from a string.
// It converts hyphens to underscores before processing and returns the trimmed result.
// If the result would be empty, it returns the original string.
func trimNumericPrefix(part string) string {
	part = strings.ReplaceAll(part, "-", "_")
	runes := []rune(part)
	i := 0
	for i < len(runes) {
		r := runes[i]
		if unicode.IsDigit(r) || r == '_' {
			i++
			continue
		}
		break
	}
	trimmed := string(runes[i:])
	if trimmed == "" {
		return part
	}
	return trimmed
}

// deriveAliasCandidate creates a package alias candidate based on the package path and identifier.
// It uses the last segment of the package path and applies special handling for common patterns
// like "workflows" and "activities" directories. For "workflows" directories, it combines the
// previous segment with "workflows". For "activities" directories, it combines the previous
// segment with "activity". Returns a sanitized alias string.
func deriveAliasCandidate(pkgPath, identifier string) string {
	sanitizedIdent := sanitizeAlias(identifier)
	segments := strings.Split(pkgPath, "/")
	candidateParts := []string{}
	if len(segments) > 0 {
		lastSegment := segments[len(segments)-1]
		previousSegment := ""
		if len(segments) > 1 {
			previousSegment = trimNumericPrefix(segments[len(segments)-2])
		}
		switch strings.ToLower(lastSegment) {
		case "workflows":
			if previousSegment != "" {
				candidateParts = append(candidateParts, previousSegment)
			}
			candidateParts = append(candidateParts, "workflows")
		case "activities":
			if previousSegment != "" {
				candidateParts = append(candidateParts, previousSegment)
			}
			candidateParts = append(candidateParts, "activity")
		default:
			candidateParts = append(candidateParts, trimNumericPrefix(lastSegment))
		}
	}

	candidate := sanitizeAlias(strings.Join(candidateParts, "_"))
	if candidate == "" || candidate == aliasFallback {
		candidate = sanitizedIdent
	}
	if candidate == "" || candidate == aliasFallback {
		candidate = sanitizeAlias(pkgPath)
	}
	return candidate
}

// assignAlias generates, ensures uniqueness, and registers a package alias.
//
// It first attempts to use the preferredDefaultName, falls back to the package base path,
// and ensures the result is globally unique across all packages by appending a number if needed.
func assignAlias(pkgPath, preferredDefaultName string) string {
	// 1. Cache Check: Return existing alias if found.
	if alias, ok := packageAlias[pkgPath]; ok {
		return alias
	}

	// 2. Base Alias Generation

	// Start with the preferred name
	base := sanitizeAlias(preferredDefaultName)

	// Fallback to the package base path if the preferred name is generic
	if base == "" || base == aliasFallback {
		base = sanitizeAlias(path.Base(pkgPath))
	}

	// Final fallback
	if base == "" {
		base = aliasFallback
	}

	// 3. Uniqueness Enforcement (De-duplication)
	alias := base

	// Check and increment the count for the base alias
	if count, ok := aliasCounts[alias]; ok {
		// Found a conflict, assign the next available number
		aliasCounts[alias] = count + 1
		alias = fmt.Sprintf("%s%d", alias, count)
	} else {
		// First time seeing this base alias
		aliasCounts[alias] = 1
	}

	// 4. Cache and Return
	packageAlias[pkgPath] = alias
	return alias
}

// hasMethod checks if a type has a method with the given name.
// It uses the type's method set to search for the specified method.
// Returns false if the type is nil.
func hasMethod(t types.Type, name string) bool {
	if t == nil {
		return false
	}
	ms := types.NewMethodSet(t)
	for i := 0; i < ms.Len(); i++ {
		if ms.At(i).Obj().Name() == name {
			return true
		}
	}
	return false
}

// isWorkflowStruct checks if a type is a valid workflow struct.
// A workflow struct must have both WorkflowName() string and Workflow() methods.
// The WorkflowName method must have no parameters and return a single string result.
// Returns false if the type is nil or doesn't meet the requirements.
func isWorkflowStruct(t types.Type) bool {
	if t == nil {
		return false
	}

	if !hasMethod(t, "WorkflowName") {
		return false
	}
	if !hasMethod(t, "Workflow") {
		return false
	}

	// Verify WorkflowName signature: func() string
	ms := types.NewMethodSet(t)
	for i := 0; i < ms.Len(); i++ {
		method := ms.At(i)
		if method.Obj().Name() == "WorkflowName" {
			sig, ok := method.Type().(*types.Signature)
			if !ok {
				return false
			}
			// Should have no parameters and one string result
			if sig.Params().Len() != 0 {
				return false
			}
			if sig.Results().Len() != 1 {
				return false
			}
			resultType := sig.Results().At(0).Type()
			if basic, ok := resultType.(*types.Basic); !ok || basic.Kind() != types.String {
				return false
			}
		}
	}

	return true
}

// processWorkflowTypeName processes a type name to check if it's a struct-based workflow
// and adds it to the result if valid. It verifies the type is a struct implementing the
// workflow interface, assigns an appropriate package alias, checks for WorkflowActivities
// method, and appends the workflow metadata to the result. Returns the assigned alias
// or empty string if the type is not a valid workflow.
func processWorkflowTypeName(
	typeName *types.TypeName,
	name string,
	pkg *packages.Package,
	result *WorkflowMetadataResult,
) string {
	named, ok := typeName.Type().(*types.Named)
	if !ok {
		return ""
	}

	// Check if it's a struct
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return ""
	}

	// Check both value and pointer receivers for methods
	t := named
	ptrT := types.NewPointer(named)

	if !isWorkflowStruct(t) && !isWorkflowStruct(ptrT) {
		return ""
	}

	alias := assignAlias(pkg.PkgPath, deriveAliasCandidate(pkg.PkgPath, name))

	hasActivitiesMethod := hasMethod(t, "WorkflowActivities") || hasMethod(ptrT, "WorkflowActivities")

	// Set workflowsdk import path if not yet set
	// Look for it in the package imports
	if result.WorkflowsdkImportPath == "" {
		for _, imp := range pkg.Imports {
			if imp.PkgPath != "" && strings.HasSuffix(imp.PkgPath, "/workflowsdk") {
				result.WorkflowsdkImportPath = imp.PkgPath
				break
			}
		}
	}

	workflowMeta := WorkflowDescriptorMeta{
		Import:     ImportSpec{Alias: alias, Path: pkg.PkgPath},
		Identifier: name,
		HasFn:      false, // struct-based workflows don't use WorkflowFn
		HasSignals: hasActivitiesMethod,
		Activities: []ActivityDescriptorMeta{}, // TODO: extract activities from WorkflowActivities method
	}

	result.Workflows = append(result.Workflows, workflowMeta)
	return alias
}

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Project instructions.",
	Long:  `Customer project instructions.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("project")
	},
}

var generateProjectCmd = &cobra.Command{
	Use:   "gen",
	Short: "Generate main file.",
	Long:  `Generate main file.`,
	Run: func(cmd *cobra.Command, args []string) {
		rootDir, err := resolveProjectRoot()
		if err != nil {
			FailWithError(err)
		}

		metadata, err := generateWorkflowMetadata(rootDir)
		if err != nil {
			FailWithError(err)
		}

		err = generateMainFile(rootDir, metadata)
		if err != nil {
			FailWithError(err)
		}

		modCmd := exec.Command("go", "mod", "tidy")
		modCmd.Dir = rootDir
		if err := commands.RunCommandWithLogs(modCmd); err != nil {
			FailWithError(err)
		}
	},
}

var buildProjectCmd = &cobra.Command{
	Use:   "build",
	Short: "Build stack.",
	Long:  `Build stack.`,
	Run: func(cmd *cobra.Command, args []string) {
		rootDir, err := resolveProjectRoot()
		if err != nil {
			FailWithError(err)
		}

		metadata, err := generateWorkflowMetadata(rootDir)
		if err != nil {
			FailWithError(err)
		}

		if err := generateMainFile(rootDir, metadata); err != nil {
			FailWithError(err)
		}

		modCmd := exec.Command("go", "mod", "tidy")
		modCmd.Dir = rootDir
		if err := commands.RunCommandWithLogs(modCmd); err != nil {
			FailWithError(err)
		}

		executableName, _ := cmd.Flags().GetString(executableFileNameFlag)

		if err := validateNoDuplicates(metadata); err != nil {
			FailWithError(err)
		}

		buildCmd := exec.Command("go", "build", "-o", executableName)
		buildCmd.Dir = rootDir
		if err := commands.RunCommandWithLogs(buildCmd); err != nil {
			FailWithError(err)
		}
	},
}

var runProjectCmd = &cobra.Command{
	Use:   "run",
	Short: "Run stack.",
	Long:  `Run stack.`,
	Run: func(cmd *cobra.Command, args []string) {
		rootDir, err := resolveProjectRoot()
		if err != nil {
			FailWithError(err)
		}

		executableName, _ := cmd.Flags().GetString(executableFileNameFlag)

		// #nosec G204 -- executableName is validated by flag parsing and rootDir is resolved from project root
		workerCmd := exec.Command(path.Join(rootDir, executableName))
		if err := commands.RunCommandWithLogs(workerCmd); err != nil {
			FailWithError(err)
		}
	},
}

// generateWorkflowMetadata analyzes the project directory to discover workflow and activity
// definitions. It scans all Go packages in the project, identifies struct-based workflows
// implementing the required interface, and extracts metadata including import paths and
// identifiers. Returns WorkflowMetadataResult containing discovered workflows, activities,
// and the workflowsdk import path, or an error if discovery fails.
func generateWorkflowMetadata(rootDir string) (WorkflowMetadataResult, error) {
	result := WorkflowMetadataResult{}

	dir := rootDir
	if dir == "" {
		var err error
		dir, err = resolveProjectRoot()
		if err != nil {
			return result, err
		}
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return result, fmt.Errorf("failed to resolve project root: %w", err)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedFiles | packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
		Dir:  absDir,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return result, fmt.Errorf("failed to load packages: %w", err)
	}

	result.WorkflowsdkImportPath = ""

	for _, pkg := range pkgs {
		if pkg == nil || pkg.Types == nil || pkg.Types.Scope() == nil {
			continue
		}
		if strings.HasSuffix(pkg.PkgPath, "/workflowsdk") {
			continue
		}

		scope := pkg.Types.Scope()

		scopeNames := scope.Names()
		sort.Strings(scopeNames)
		for _, name := range scopeNames {
			obj := scope.Lookup(name)
			if obj == nil || !obj.Exported() {
				continue
			}

			// Check for struct-based workflows (new pattern)
			if typeName, ok := obj.(*types.TypeName); ok {
				processWorkflowTypeName(typeName, name, pkg, &result)
			}
		}
	}

	sort.Slice(result.Workflows, func(i, j int) bool {
		left := result.Workflows[i]
		right := result.Workflows[j]
		if left.Import.Path == right.Import.Path {
			return left.Identifier < right.Identifier
		}
		return left.Import.Path < right.Import.Path
	})

	sort.Slice(result.Activities, func(i, j int) bool {
		left := result.Activities[i]
		right := result.Activities[j]
		if left.Import.Path == right.Import.Path {
			return left.Identifier < right.Identifier
		}
		return left.Import.Path < right.Import.Path
	})

	if result.WorkflowsdkImportPath == "" {
		return result, fmt.Errorf("%w: ensure struct-based workflows with WorkflowSignals() []Signal method are present", errWorkflowsdkDetection)
	}

	return result, nil
}

// generateMainFile generates the main.go file for the workflow project using the provided
// metadata. It creates import statements for all workflow and activity packages, builds
// lists of workflow and activity registrations, and executes the main template to produce
// the final file. Returns an error if the workflowsdk import path is missing, module path
// resolution fails, or file generation encounters issues.
func generateMainFile(rootDir string, metadata WorkflowMetadataResult) error {
	if metadata.WorkflowsdkImportPath == "" {
		return errWorkflowsdkMetadataUnset
	}

	modulePath, err := resolveModulePath(rootDir)
	if err != nil {
		return err
	}

	importsMap := map[string]string{}
	for _, wf := range metadata.Workflows {
		if wf.Import.Path == metadata.WorkflowsdkImportPath {
			continue
		}
		importsMap[wf.Import.Path] = wf.Import.Alias
	}
	for _, act := range metadata.Activities {
		if act.Import.Path == metadata.WorkflowsdkImportPath {
			continue
		}
		importsMap[act.Import.Path] = act.Import.Alias
	}
	importsMap[metadata.WorkflowsdkImportPath] = "workflowsdk"

	importPaths := make([]string, 0, len(importsMap))
	for p := range importsMap {
		importPaths = append(importPaths, p)
	}
	sort.Strings(importPaths)

	importLines := make([]string, 0, len(importsMap)+1)
	for _, p := range importPaths {
		alias := importsMap[p]
		if alias != "" {
			importLines = append(importLines, fmt.Sprintf("%s %q", alias, p))
		} else {
			importLines = append(importLines, fmt.Sprintf("%q", p))
		}
	}

	importLines = append(importLines, fmt.Sprintf("%q", modulePath+"/pyck_client"))

	if len(metadata.Workflows) == 0 {
		return errNoWorkflowsDiscovered
	}

	workflowsListEntries := make([]string, 0, len(metadata.Workflows))
	for _, wf := range metadata.Workflows {
		alias := wf.Import.Alias
		if alias == "" {
			alias = path.Base(wf.Import.Path)
		}
		// For struct-based workflows (HasFn == false), instantiate with {}
		// For descriptor-based workflows (HasFn == true), reference the variable
		if wf.HasFn {
			// Descriptor-based: reference variable
			workflowsListEntries = append(workflowsListEntries, fmt.Sprintf("%s.%s", alias, wf.Identifier))
		} else {
			// Struct-based: instantiate type
			workflowsListEntries = append(workflowsListEntries, fmt.Sprintf("%s.%s{}", alias, wf.Identifier))
		}
	}

	workflowKeys := make([]string, 0, len(metadata.Workflows))
	for _, wf := range metadata.Workflows {
		workflowKeys = append(workflowKeys, fmt.Sprintf("%s.%s", wf.Import.Path, wf.Identifier))
	}
	if dups := findStringDuplicates(workflowKeys); len(dups) > 0 {
		log.Printf("WARN: duplicate workflows found: %v", dups)
	}

	activitiesListEntries := make([]string, 0, len(metadata.Activities))
	for _, act := range metadata.Activities {
		alias := act.Import.Alias
		if alias == "" {
			alias = path.Base(act.Import.Path)
		}
		activitiesListEntries = append(activitiesListEntries, fmt.Sprintf("%s.%s", alias, act.Identifier))
	}

	data := mainTemplateData{
		Imports:    importLines,
		Workflows:  workflowsListEntries,
		Activities: activitiesListEntries,
	}

	var buf bytes.Buffer
	if err := mainTemplates.ExecuteTemplate(&buf, mainTemplateName, data); err != nil {
		return fmt.Errorf("failed to execute main template: %w", err)
	}

	mainFilePath := filepath.Join(rootDir, "main.go")
	return os.WriteFile(mainFilePath, buf.Bytes(), 0o600)
}

// validateNoDuplicates checks the workflow and activity metadata for duplicate entries.
// It constructs unique keys from import paths and identifiers, then verifies no duplicates
// exist. Returns errDuplicateWorkflows or errDuplicateActivities with the list of duplicates
// if any are found, otherwise returns nil.
func validateNoDuplicates(metadata WorkflowMetadataResult) error {
	workflowKeys := make([]string, 0, len(metadata.Workflows))
	for _, wf := range metadata.Workflows {
		workflowKeys = append(workflowKeys, fmt.Sprintf("%s.%s", wf.Import.Path, wf.Identifier))
	}
	if dups := findStringDuplicates(workflowKeys); len(dups) > 0 {
		return fmt.Errorf("%w: %v", errDuplicateWorkflows, dups)
	}

	activityKeys := make([]string, 0, len(metadata.Activities))
	for _, act := range metadata.Activities {
		activityKeys = append(activityKeys, fmt.Sprintf("%s.%s", act.Import.Path, act.Identifier))
	}
	if dups := findStringDuplicates(activityKeys); len(dups) > 0 {
		return fmt.Errorf("%w: %v", errDuplicateActivities, dups)
	}

	return nil
}

// findStringDuplicates identifies duplicate strings in the provided array.
// It returns a slice containing all strings that appear more than once,
// with duplicates appearing in the result for each occurrence after the first.
func findStringDuplicates(arr []string) []string {
	seen := make(map[string]struct{})
	duplicates := []string{}
	for _, str := range arr {
		_, ok := seen[str]
		if ok {
			duplicates = append(duplicates, str)
		}
		seen[str] = struct{}{}
	}
	return duplicates
}

// resolveModulePath reads the go.mod file in the specified root directory and extracts
// the module path. It scans the file for the "module" directive and returns the module
// name. Returns errModulePathNotFound if the module directive is missing or invalid.
func resolveModulePath(rootDir string) (string, error) {
	modFilePath := filepath.Join(rootDir, "go.mod")

	file, err := os.Open(modFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read go.mod: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "module ") {
			module := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			if module == "" {
				break
			}

			return module, nil
		}
	}

	return "", fmt.Errorf("%w in %s", errModulePathNotFound, modFilePath)
}
