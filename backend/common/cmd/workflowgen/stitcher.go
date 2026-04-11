package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// Stitcher combines multiple RFW component files into a single output file.
// It deduplicates imports and adds metadata about the source workflows.
type Stitcher struct {
	fs             FileSystem
	bpmnParser     *BPMNParser
	template       string
	verbose        bool
	dryRun         bool
	defaultImports []string
}

// WorkflowComponents represents all RFW components from a single workflow.
type WorkflowComponents struct {
	WorkflowName string         // Workflow name from BPMN filename (e.g., "101-unloading")
	BPMNFile     string         // Source BPMN file path
	RfwFile      string         // Source RFW file path
	Components   []RfwComponent // All components from this workflow
}

// WidgetOccurrence tracks where a widget was first defined.
type WidgetOccurrence struct {
	WorkflowName string
	RfwFile      string
	Content      string
}

// StitchedOutput represents the final combined output.
type StitchedOutput struct {
	TotalComponents int                  // Total number of components
	TotalWorkflows  int                  // Total number of workflows
	Workflows       []WorkflowComponents // All workflows with their components
	Imports         []string             // Deduplicated imports
}

// NewStitcher creates a new stitcher with the given dependencies.
func NewStitcher(fs FileSystem, template string, verbose bool) *Stitcher {
	return &Stitcher{
		fs:         fs,
		bpmnParser: NewBPMNParser(fs, verbose),
		template:   template,
		verbose:    verbose,
		dryRun:     false,
		defaultImports: []string{
			"core.widgets",
			"local",
		},
	}
}

// StitchWidgets finds all RFW component files and combines them into a single output file.
// The output file is written to the specified outputPath (e.g., "dist/widgets.rfwtxt").
func (s *Stitcher) StitchWidgets(searchPath, outputPath string) error {
	// Find all flutter/index.rfwtxt files
	rfwFiles, err := s.findAllRfwFiles(searchPath)
	if err != nil {
		return fmt.Errorf("failed to find RFW files: %w", err)
	}

	if len(rfwFiles) == 0 {
		if s.verbose {
			fmt.Println("No RFW component files found")
		}
		return nil
	}

	if s.verbose {
		fmt.Printf("Found %d RFW component file(s) to stitch\n", len(rfwFiles))
	}

	// Parse all component files and collect workflows
	var allWorkflows []WorkflowComponents
	allImports := make(map[string]bool)
	// Add default imports
	for _, imp := range s.defaultImports {
		allImports[imp] = true
	}

	for _, rfwFile := range rfwFiles {
		workflow, imports, err := s.parseWorkflowFile(rfwFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", rfwFile, err)
		}

		if len(workflow.Components) > 0 {
			allWorkflows = append(allWorkflows, workflow)

			// Collect imports
			for _, imp := range imports {
				allImports[imp] = true
			}

			if s.verbose {
				fmt.Printf("  - %s: %d component(s) from %s\n", workflow.WorkflowName, len(workflow.Components), rfwFile)
			}
		}
	}

	// Sort workflows by name for consistent output
	sort.Slice(allWorkflows, func(i, j int) bool {
		return allWorkflows[i].WorkflowName < allWorkflows[j].WorkflowName
	})

	// Deduplicate widgets across workflows
	seenWidgets := make(map[string]WidgetOccurrence) // widgetID -> first occurrence
	var warnings []string
	duplicatesSkipped := 0

	for i := range allWorkflows {
		var keptComponents []RfwComponent
		for _, comp := range allWorkflows[i].Components {
			if existing, seen := seenWidgets[comp.ID]; seen {
				// Duplicate found
				duplicatesSkipped++
				if existing.Content != comp.Content {
					// Conflict - warn
					warnings = append(warnings, fmt.Sprintf(
						"Widget %q defined in both %s and %s with different content (keeping %s)",
						comp.ID, existing.WorkflowName, allWorkflows[i].WorkflowName, existing.WorkflowName,
					))
				}
				// Skip duplicate (identical or conflicting)
				continue
			}
			// First occurrence - keep it
			seenWidgets[comp.ID] = WidgetOccurrence{
				WorkflowName: allWorkflows[i].WorkflowName,
				RfwFile:      allWorkflows[i].RfwFile,
				Content:      comp.Content,
			}
			keptComponents = append(keptComponents, comp)
		}
		allWorkflows[i].Components = keptComponents
	}

	// Print warnings for conflicting widgets
	for _, w := range warnings {
		fmt.Printf("WARNING: %s\n", w)
	}

	// Convert imports map to sorted slice
	importSlice := make([]string, 0, len(allImports))
	for imp := range allImports {
		importSlice = append(importSlice, imp)
	}
	sort.Strings(importSlice)

	// Count total components
	totalComponents := 0
	for _, wf := range allWorkflows {
		totalComponents += len(wf.Components)
	}

	// Prepare output data
	output := StitchedOutput{
		TotalComponents: totalComponents,
		TotalWorkflows:  len(allWorkflows),
		Workflows:       allWorkflows,
		Imports:         importSlice,
	}

	// Generate output content
	content, err := s.generateOutput(output)
	if err != nil {
		return fmt.Errorf("failed to generate output: %w", err)
	}

	// Handle dry-run mode
	if s.dryRun {
		fmt.Printf("=== Generated %s (dry-run mode) ===\n", outputPath)
		fmt.Println(content)
		fmt.Println("=== End of generated output ===")
		return nil
	}

	// Ensure output directory exists
	outputDir := filepath.Dir(outputPath)
	if err := s.ensureDir(outputDir); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Write output file
	if err := s.fs.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}

	if s.verbose {
		fmt.Printf("\nSuccessfully generated %s\n", outputPath)
		fmt.Printf("  Total workflows: %d\n", len(allWorkflows))
		fmt.Printf("  Unique components: %d\n", totalComponents)
		if duplicatesSkipped > 0 {
			fmt.Printf("  Duplicates skipped: %d\n", duplicatesSkipped)
		}
	}

	return nil
}

// findAllRfwFiles finds all .rfwtxt files in the search path, regardless of directory structure.
// It skips files under dist/ to avoid stitching our own output.
func (s *Stitcher) findAllRfwFiles(searchPath string) ([]string, error) {
	var rfwFiles []string

	// Check if recursive search is requested
	recursive := strings.HasSuffix(searchPath, "/...")
	basePath := searchPath
	if recursive {
		basePath = strings.TrimSuffix(searchPath, "/...")
		if basePath == "" {
			basePath = "."
		}
	}

	var err error
	if recursive {
		rfwFiles, err = s.findRfwFilesRecursive(basePath)
	} else {
		rfwFiles, err = s.findRfwFilesNonRecursive(basePath)
	}
	if err != nil {
		return nil, err
	}

	// Sort for consistent output
	sort.Strings(rfwFiles)

	return rfwFiles, nil
}

// findRfwFilesRecursive recursively finds all .rfwtxt files, skipping dist/ directories.
func (s *Stitcher) findRfwFilesRecursive(basePath string) ([]string, error) {
	var rfwFiles []string
	err := s.fs.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, ".rfwtxt") {
			return nil
		}
		// Skip files under dist/ to avoid stitching our own output
		if strings.Contains(path, "/dist/") || strings.HasPrefix(path, "dist/") {
			return nil
		}
		rfwFiles = append(rfwFiles, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}
	return rfwFiles, nil
}

// findRfwFilesNonRecursive finds .rfwtxt files in immediate subdirectories.
func (s *Stitcher) findRfwFilesNonRecursive(basePath string) ([]string, error) {
	entries, err := s.fs.ReadDir(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var rfwFiles []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Walk each immediate subdirectory to find .rfwtxt files
		subdir := s.fs.Join(basePath, entry.Name())
		subEntries, err := s.fs.ReadDir(subdir)
		if err != nil {
			continue
		}
		for _, subEntry := range subEntries {
			if subEntry.IsDir() {
				// Check one level deeper (e.g., mobile/*.rfwtxt)
				deepDir := s.fs.Join(subdir, subEntry.Name())
				deepEntries, err := s.fs.ReadDir(deepDir)
				if err != nil {
					continue
				}
				for _, deepEntry := range deepEntries {
					if !deepEntry.IsDir() && strings.HasSuffix(deepEntry.Name(), ".rfwtxt") {
						rfwFiles = append(rfwFiles, s.fs.Join(deepDir, deepEntry.Name()))
					}
				}
			} else if strings.HasSuffix(subEntry.Name(), ".rfwtxt") {
				rfwFiles = append(rfwFiles, s.fs.Join(subdir, subEntry.Name()))
			}
		}
	}
	return rfwFiles, nil
}

// parseWorkflowFile parses a single .rfwtxt file.
// Returns the workflow components, imports, and any error.
// All widgets found in the file are included (no BPMN filtering).
func (s *Stitcher) parseWorkflowFile(rfwFile string) (WorkflowComponents, []string, error) {
	// Find corresponding BPMN file for metadata only
	bpmnFile := s.findBPMNFile(rfwFile)
	workflowName := s.extractWorkflowNameFromRfwPath(rfwFile)

	// Read RFW file content
	content, err := s.fs.ReadFile(rfwFile)
	if err != nil {
		return WorkflowComponents{}, nil, fmt.Errorf("failed to read file: %w", err)
	}

	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	var allWidgets []RfwComponent
	var imports []string
	var currentComponent *RfwComponent
	var componentLines []string
	inComponent := false

	// Pattern to match widget declarations and imports
	componentPattern := regexp.MustCompile(`^widget\s+(\w+)\s*=`)
	importPattern := regexp.MustCompile(`^import\s+([^;]+);`)

	for _, line := range lines {
		// Check for imports
		if matches := importPattern.FindStringSubmatch(line); matches != nil {
			importPath := strings.TrimSpace(matches[1])
			imports = append(imports, importPath)
			continue
		}

		// Check if this line starts a component
		if matches := componentPattern.FindStringSubmatch(line); matches != nil {
			// Save previous component if any
			if currentComponent != nil {
				currentComponent.Content = strings.Join(componentLines, "\n")
				allWidgets = append(allWidgets, *currentComponent)
			}

			// Start new component
			currentComponent = &RfwComponent{
				ID: matches[1],
			}
			componentLines = []string{line}
			inComponent = true
			continue
		}

		if inComponent {
			componentLines = append(componentLines, line)

			// Check if component declaration is complete
			trimmed := strings.TrimSpace(line)
			if trimmed == ");" {
				currentComponent.Content = strings.Join(componentLines, "\n")
				allWidgets = append(allWidgets, *currentComponent)
				currentComponent = nil
				componentLines = nil
				inComponent = false
			}
		}
	}

	// Save last component if any
	if currentComponent != nil {
		currentComponent.Content = strings.Join(componentLines, "\n")
		allWidgets = append(allWidgets, *currentComponent)
	}

	workflow := WorkflowComponents{
		WorkflowName: workflowName,
		BPMNFile:     bpmnFile,
		RfwFile:      rfwFile,
		Components:   allWidgets,
	}

	return workflow, imports, nil
}

// extractWorkflowName extracts workflow name from the BPMN file path.
// Uses the BPMN filename (without .bpmn extension) as the workflow name.
// Example: "workflows/101-unloading/101-unloading.bpmn" -> "101-unloading"
// Example: "a/b/c/d/e/f/receiving.bpmn" -> "receiving"
func extractWorkflowName(bpmnPath string) string {
	if bpmnPath == "" {
		return "unknown"
	}

	baseName := filepath.Base(bpmnPath)
	return strings.TrimSuffix(baseName, ".bpmn")
}

// extractWorkflowNameFromRfwPath extracts the workflow name from an .rfwtxt file path.
// It walks up directories to find the workflow root (the directory containing a .bpmn file),
// or falls back to using the parent directory name.
// Example: "workflows/201-picking/mobile/scan-tray.rfwtxt" -> "201-picking"
// Example: "workflows/201-picking/flutter/index.rfwtxt" -> "201-picking"
func (s *Stitcher) extractWorkflowNameFromRfwPath(rfwPath string) string {
	// Walk up from the .rfwtxt file looking for a .bpmn file
	dir := filepath.Dir(rfwPath)
	for range 5 { // max 5 levels up
		entries, err := s.fs.ReadDir(dir)
		if err != nil {
			break
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".bpmn") {
				return filepath.Base(dir)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}
	// Fallback: use the grandparent directory name
	// e.g., workflows/201-picking/mobile/file.rfwtxt -> 201-picking
	return filepath.Base(filepath.Dir(filepath.Dir(rfwPath)))
}

// findBPMNFile finds the BPMN file corresponding to an .rfwtxt file.
// It walks up directories from the .rfwtxt file looking for a .bpmn file.
func (s *Stitcher) findBPMNFile(rfwPath string) string {
	dir := filepath.Dir(rfwPath)
	for range 5 { // max 5 levels up
		entries, err := s.fs.ReadDir(dir)
		if err != nil {
			break
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".bpmn") {
				return s.fs.Join(dir, entry.Name())
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}
	return ""
}

// generateOutput generates the final stitched output using the template.
func (s *Stitcher) generateOutput(output StitchedOutput) (string, error) {
	// Parse template
	tmpl, err := template.New("stitched").Parse(s.template)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Execute template
	var buf strings.Builder
	if err := tmpl.Execute(&buf, output); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// ensureDir ensures that a directory exists, creating it if necessary.
func (s *Stitcher) ensureDir(dir string) error {
	// Check if directory exists
	_, err := s.fs.ReadDir(dir)
	if err == nil {
		return nil
	}

	// For osFileSystem, create the directory
	if _, ok := s.fs.(osFileSystem); ok {
		return os.MkdirAll(dir, 0o755)
	}

	// For memFileSystem, directories are implicit
	return nil
}
