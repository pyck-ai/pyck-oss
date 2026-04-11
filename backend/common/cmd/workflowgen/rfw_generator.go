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

// RfwGenerator handles synchronization of RFW components with BPMN user tasks.
// It uses BPMN as the source of truth and generates/updates RFW component files accordingly.
type RfwGenerator struct {
	fs          FileSystem
	bpmnParser  *BPMNParser
	rfwTemplate string
	verbose     bool
	dryRun      bool
}

// RfwComponent represents a parsed RFW component from an RFW file.
type RfwComponent struct {
	ID      string // Component identifier (e.g., "Activity_0uj4ej4")
	Content string // Full component definition including comments
}

// RfwDiff represents the differences between BPMN and existing RFW components.
type RfwDiff struct {
	ToAdd    []UserTask     // User tasks that need new component scaffolds
	Orphaned []RfwComponent // Components in RFW but not in BPMN (kept but excluded from dist)
	ToKeep   []RfwComponent // Components that exist in both (preserve implementation)
}

// NewWidgetGenerator creates a new RFW generator with the given dependencies.
// Kept for backward compatibility; use NewRfwGenerator for new code.
func NewWidgetGenerator(fs FileSystem, bpmnParser *BPMNParser, rfwTemplate string, verbose bool) *RfwGenerator {
	return &RfwGenerator{
		fs:          fs,
		bpmnParser:  bpmnParser,
		rfwTemplate: rfwTemplate,
		verbose:     verbose,
		dryRun:      false,
	}
}

// NewRfwGenerator creates a new RFW generator with the given dependencies.
func NewRfwGenerator(fs FileSystem, bpmnParser *BPMNParser, rfwTemplate string, verbose bool) *RfwGenerator {
	return &RfwGenerator{
		fs:          fs,
		bpmnParser:  bpmnParser,
		rfwTemplate: rfwTemplate,
		verbose:     verbose,
		dryRun:      false,
	}
}

// SyncWidgets synchronizes an RFW component file with its corresponding BPMN file.
// It adds missing components, removes orphaned components, and preserves existing implementations.
func (g *RfwGenerator) SyncWidgets(bpmnFile, rfwFile string) (int, int, error) {
	// Parse BPMN to get user tasks
	userTasks, err := g.bpmnParser.ParseBPMN(bpmnFile)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse BPMN: %w", err)
	}

	// Parse existing RFW file (if it exists)
	existingComponents, rfwHeader, err := g.parseRfwFile(rfwFile)
	if err != nil && !os.IsNotExist(err) {
		return 0, 0, fmt.Errorf("failed to parse existing RFW file: %w", err)
	}

	// Calculate diff
	diff := g.calculateDiff(userTasks, existingComponents)

	// Generate new RFW file content
	newContent, err := g.generateRfwFile(rfwHeader, diff, userTasks)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to generate RFW file: %w", err)
	}

	// Report changes
	if g.verbose {
		if len(diff.ToAdd) > 0 {
			fmt.Printf("  Adding %d component(s):\n", len(diff.ToAdd))
			for _, task := range diff.ToAdd {
				fmt.Printf("    + %s (%s)\n", task.ID, task.Name)
			}
		}
		if len(diff.ToKeep) > 0 {
			fmt.Printf("  Preserving %d existing component(s)\n", len(diff.ToKeep))
		}
	}

	// Always warn about orphaned widgets (not in BPMN, excluded from dist)
	if len(diff.Orphaned) > 0 {
		fmt.Printf("  WARNING: %d orphaned widget(s) not linked to BPMN (will be excluded from dist):\n", len(diff.Orphaned))
		for _, component := range diff.Orphaned {
			fmt.Printf("    ! %s\n", component.ID)
		}
	}

	// Handle dry-run mode
	if g.dryRun {
		fmt.Printf("=== Generated %s (dry-run mode) ===\n", rfwFile)
		fmt.Println(newContent)
		fmt.Println("=== End of generated output ===")
		return len(diff.ToAdd), len(diff.Orphaned), nil
	}

	// Ensure the flutter directory exists
	rfwDir := filepath.Dir(rfwFile)
	if err := g.ensureDir(rfwDir); err != nil {
		return 0, 0, fmt.Errorf("failed to create directory %s: %w", rfwDir, err)
	}

	// Write updated content
	if err := g.fs.WriteFile(rfwFile, []byte(newContent), 0o644); err != nil {
		return 0, 0, fmt.Errorf("failed to write RFW file: %w", err)
	}

	return len(diff.ToAdd), len(diff.Orphaned), nil
}

// parseRfwFile parses an existing RFW component file and extracts components.
// Returns the components, the file header (imports), and any error.
func (g *RfwGenerator) parseRfwFile(rfwFile string) ([]RfwComponent, string, error) {
	content, err := g.fs.ReadFile(rfwFile)
	if err != nil {
		return nil, "", err
	}

	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	var components []RfwComponent
	var headerLines []string
	var currentComponent *RfwComponent
	var componentLines []string
	inComponent := false

	// Pattern to match widget declarations: "widget <ID> = "
	componentStartPattern := regexp.MustCompile(`^widget\s+(\w+)\s*=`)

	for _, line := range lines {
		// Check if this line starts a component
		matches := componentStartPattern.FindStringSubmatch(line)
		if matches != nil {
			// Save previous component if any
			if currentComponent != nil {
				currentComponent.Content = strings.Join(componentLines, "\n")
				components = append(components, *currentComponent)
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
			inComponent = g.processComponentLine(line, &componentLines, &currentComponent, &components)
		} else {
			g.processHeaderLine(line, &headerLines, &components, currentComponent)
		}
	}

	// Save last component if any
	if currentComponent != nil {
		currentComponent.Content = strings.Join(componentLines, "\n")
		components = append(components, *currentComponent)
	}

	// Clean up header (remove trailing blank lines)
	for len(headerLines) > 0 && strings.TrimSpace(headerLines[len(headerLines)-1]) == "" {
		headerLines = headerLines[:len(headerLines)-1]
	}

	header := strings.Join(headerLines, "\n")

	return components, header, nil
}

// processComponentLine processes a line within a component block.
// Returns true if still in component, false if component is complete.
func (g *RfwGenerator) processComponentLine(line string, componentLines *[]string, currentComponent **RfwComponent, components *[]RfwComponent) bool {
	*componentLines = append(*componentLines, line)
	trimmed := strings.TrimSpace(line)
	if trimmed == ");" {
		(*currentComponent).Content = strings.Join(*componentLines, "\n")
		*components = append(*components, **currentComponent)
		*currentComponent = nil
		*componentLines = nil
		return false
	}
	return true
}

// processHeaderLine processes a line before any components (header section).
func (g *RfwGenerator) processHeaderLine(line string, headerLines *[]string, components *[]RfwComponent, currentComponent *RfwComponent) {
	if len(*components) == 0 && currentComponent == nil {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" || len(*headerLines) > 0 {
			if !strings.HasPrefix(trimmed, "// AUTO-GENERATED") &&
				!strings.HasPrefix(trimmed, "// Generated by") &&
				!strings.HasPrefix(trimmed, "// Source:") {
				*headerLines = append(*headerLines, line)
			}
		}
	}
}

// calculateDiff compares BPMN user tasks with existing RFW components.
func (g *RfwGenerator) calculateDiff(userTasks []UserTask, existingComponents []RfwComponent) RfwDiff {
	// Create maps for quick lookup
	bpmnTaskMap := make(map[string]UserTask)
	for _, task := range userTasks {
		bpmnTaskMap[task.ID] = task
	}

	componentMap := make(map[string]RfwComponent)
	for _, component := range existingComponents {
		componentMap[component.ID] = component
	}

	var diff RfwDiff

	// Find components to add (in BPMN but not in RFW)
	for _, task := range userTasks {
		if _, exists := componentMap[task.ID]; !exists {
			diff.ToAdd = append(diff.ToAdd, task)
		}
	}

	// Find orphaned components (in RFW but not in BPMN) - kept but excluded from dist
	for _, component := range existingComponents {
		if _, exists := bpmnTaskMap[component.ID]; !exists {
			diff.Orphaned = append(diff.Orphaned, component)
		}
	}

	// Find components to keep (in both)
	for _, component := range existingComponents {
		if _, exists := bpmnTaskMap[component.ID]; exists {
			diff.ToKeep = append(diff.ToKeep, component)
		}
	}

	// Sort for consistent output
	sort.Slice(diff.ToAdd, func(i, j int) bool {
		return diff.ToAdd[i].ID < diff.ToAdd[j].ID
	})
	sort.Slice(diff.ToKeep, func(i, j int) bool {
		return diff.ToKeep[i].ID < diff.ToKeep[j].ID
	})

	return diff
}

// generateRfwFile generates the complete RFW file content with components.
func (g *RfwGenerator) generateRfwFile(header string, diff RfwDiff, allTasks []UserTask) (string, error) {
	var result strings.Builder

	// Write header (imports) if it exists
	if header != "" {
		result.WriteString(header)
		result.WriteString("\n\n")
	}

	// Combine existing components and new components, maintaining BPMN order
	taskOrder := make(map[string]int)
	for i, task := range allTasks {
		taskOrder[task.ID] = i
	}

	// Collect all components (existing + new)
	type componentEntry struct {
		id      string
		content string
		order   int
	}
	allComponents := make([]componentEntry, 0, len(diff.ToKeep)+len(diff.ToAdd))

	// Add existing components that should be kept
	for _, component := range diff.ToKeep {
		allComponents = append(allComponents, componentEntry{
			id:      component.ID,
			content: component.Content,
			order:   taskOrder[component.ID],
		})
	}

	// Add new components from templates
	for _, task := range diff.ToAdd {
		componentContent, err := g.generateRfwScaffold(task)
		if err != nil {
			return "", fmt.Errorf("failed to generate scaffold for %s: %w", task.ID, err)
		}
		allComponents = append(allComponents, componentEntry{
			id:      task.ID,
			content: componentContent,
			order:   taskOrder[task.ID],
		})
	}

	// Sort components by BPMN order
	sort.Slice(allComponents, func(i, j int) bool {
		return allComponents[i].order < allComponents[j].order
	})

	// Write all BPMN-linked components
	for i, component := range allComponents {
		if i > 0 {
			result.WriteString("\n")
		}
		result.WriteString(component.content)
		result.WriteString("\n")
	}

	// Write orphaned components (not in BPMN) - these will be excluded from dist at stitch time
	// The stitcher determines orphaned status by comparing widgets to BPMN tasks
	for _, component := range diff.Orphaned {
		result.WriteString("\n")
		result.WriteString(component.Content)
		result.WriteString("\n")
	}

	return result.String(), nil
}

// generateRfwScaffold generates a new RFW component scaffold from a user task.
func (g *RfwGenerator) generateRfwScaffold(task UserTask) (string, error) {
	// Parse template
	tmpl, err := template.New("rfw").Parse(g.rfwTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse RFW template: %w", err)
	}

	// Execute template
	var buf strings.Builder
	data := map[string]interface{}{
		"ID":       task.ID,
		"Name":     task.Name,
		"BPMNFile": task.BPMNFile,
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute RFW template: %w", err)
	}

	return buf.String(), nil
}

// ensureDir ensures that a directory exists, creating it if necessary.
func (g *RfwGenerator) ensureDir(dir string) error {
	// Check if directory exists by trying to read it
	_, err := g.fs.ReadDir(dir)
	if err == nil {
		// Directory exists
		return nil
	}

	// For osFileSystem, we can create the directory
	if _, ok := g.fs.(osFileSystem); ok {
		return os.MkdirAll(dir, 0o755)
	}

	// For memFileSystem, we don't need to create directories explicitly
	// They're implicitly created when files are written
	return nil
}
