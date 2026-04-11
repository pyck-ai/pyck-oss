package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// BPMNDefinitions represents the root element of a BPMN 2.0 XML file.
type BPMNDefinitions struct {
	XMLName         xml.Name             `xml:"definitions"`
	Processes       []BPMNProcess        `xml:"process"`
	Collaborations  []BPMNCollaboration  `xml:"collaboration"`
	TextAnnotations []BPMNTextAnnotation `xml:"textAnnotation"`
	Associations    []BPMNAssociation    `xml:"association"`
}

// BPMNCollaboration represents a BPMN collaboration element (used by Camunda Modeler).
type BPMNCollaboration struct {
	ID              string               `xml:"id,attr"`
	TextAnnotations []BPMNTextAnnotation `xml:"textAnnotation"`
	Associations    []BPMNAssociation    `xml:"association"`
}

// BPMNProcess represents a BPMN process element.
type BPMNProcess struct {
	ID              string               `xml:"id,attr"`
	Name            string               `xml:"name,attr"`
	UserTasks       []BPMNUserTask       `xml:"userTask"`
	TextAnnotations []BPMNTextAnnotation `xml:"textAnnotation"`
	Associations    []BPMNAssociation    `xml:"association"`
}

// BPMNUserTask represents a BPMN user task element.
type BPMNUserTask struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

// BPMNTextAnnotation represents a BPMN text annotation element.
type BPMNTextAnnotation struct {
	ID   string `xml:"id,attr"`
	Text string `xml:"text"`
}

// BPMNAssociation represents a BPMN association element.
type BPMNAssociation struct {
	ID        string `xml:"id,attr"`
	SourceRef string `xml:"sourceRef,attr"`
	TargetRef string `xml:"targetRef,attr"`
}

// UserTask represents a parsed user task with workflow context.
type UserTask struct {
	ID       string // User task ID from BPMN (e.g., "Activity_0uj4ej4")
	Name     string // Human-readable task name
	BPMNFile string // Path to the source BPMN file
}

// BPMNParser provides functionality to parse BPMN files and extract user tasks.
type BPMNParser struct {
	fs      FileSystem
	verbose bool
}

// NewBPMNParser creates a new BPMN parser with the given file system.
func NewBPMNParser(fs FileSystem, verbose bool) *BPMNParser {
	return &BPMNParser{
		fs:      fs,
		verbose: verbose,
	}
}

// ParseBPMN parses a BPMN file and extracts all user tasks.
// Returns a slice of UserTask structs with workflow context.
func (p *BPMNParser) ParseBPMN(bpmnPath string) ([]UserTask, error) {
	// Read BPMN file content
	content, err := p.fs.ReadFile(bpmnPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read BPMN file %s: %w", bpmnPath, err)
	}

	// Parse XML
	var definitions BPMNDefinitions
	if err := xml.Unmarshal(content, &definitions); err != nil {
		return nil, fmt.Errorf("failed to parse BPMN XML in %s: %w", bpmnPath, err)
	}

	// Build maps of text annotations and associations
	textAnnotations := make(map[string]string) // annotation ID -> text
	associations := make(map[string]string)    // sourceRef (task ID) -> targetRef (annotation ID)

	// Collect text annotations and associations from all levels:
	// 1. Definitions level
	for _, annotation := range definitions.TextAnnotations {
		textAnnotations[annotation.ID] = strings.TrimSpace(annotation.Text)
	}
	for _, assoc := range definitions.Associations {
		associations[assoc.SourceRef] = assoc.TargetRef
	}

	// 2. Collaboration level (used by Camunda Modeler)
	for _, collab := range definitions.Collaborations {
		for _, annotation := range collab.TextAnnotations {
			textAnnotations[annotation.ID] = strings.TrimSpace(annotation.Text)
		}
		for _, assoc := range collab.Associations {
			associations[assoc.SourceRef] = assoc.TargetRef
		}
	}

	// 3. Process level
	for _, process := range definitions.Processes {
		for _, annotation := range process.TextAnnotations {
			textAnnotations[annotation.ID] = strings.TrimSpace(annotation.Text)
		}
		for _, assoc := range process.Associations {
			associations[assoc.SourceRef] = assoc.TargetRef
		}
	}

	// Extract ONLY user tasks that have "ID: " annotation
	var userTasks []UserTask
	for _, process := range definitions.Processes {
		for _, task := range process.UserTasks {
			// Check if this task has an associated annotation
			annotationID, hasAssoc := associations[task.ID]
			if !hasAssoc {
				continue // Skip tasks without annotations
			}

			// Get the annotation text
			annotationText, exists := textAnnotations[annotationID]
			if !exists {
				continue // Skip if annotation doesn't exist
			}

			// Extract component ID from annotation text (format: "ID: ComponentName")
			componentID := extractComponentID(annotationText)
			if componentID == "" {
				continue // Skip if no valid ID found
			}

			// This task has a valid component ID annotation - include it
			userTasks = append(userTasks, UserTask{
				ID:       componentID, // Use extracted component ID as widget ID
				Name:     task.Name,
				BPMNFile: bpmnPath,
			})
		}
	}

	if p.verbose {
		fmt.Printf("Found %d user task(s) in %s:\n", len(userTasks), bpmnPath)
		for _, task := range userTasks {
			fmt.Printf("  - %s: %s\n", task.ID, task.Name)
		}
	}

	return userTasks, nil
}

// FindAllBPMNFiles recursively finds all .bpmn files in the given search path.
// Supports both recursive (path/...) and non-recursive (path) searches.
func (p *BPMNParser) FindAllBPMNFiles(searchPath string) ([]string, error) {
	var bpmnFiles []string

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
		bpmnFiles, err = p.findBPMNFilesRecursive(basePath)
	} else {
		bpmnFiles, err = p.findBPMNFilesNonRecursive(basePath)
	}
	if err != nil {
		return nil, err
	}

	if p.verbose && len(bpmnFiles) > 0 {
		fmt.Printf("Found %d BPMN file(s):\n", len(bpmnFiles))
		for _, file := range bpmnFiles {
			fmt.Printf("  - %s\n", file)
		}
	}

	return bpmnFiles, nil
}

// findBPMNFilesRecursive recursively finds BPMN files in a directory tree.
func (p *BPMNParser) findBPMNFilesRecursive(basePath string) ([]string, error) {
	var bpmnFiles []string
	err := p.fs.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".bpmn") {
			bpmnFiles = append(bpmnFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", basePath, err)
	}
	return bpmnFiles, nil
}

// findBPMNFilesNonRecursive finds BPMN files in a single directory (not recursive).
func (p *BPMNParser) findBPMNFilesNonRecursive(basePath string) ([]string, error) {
	entries, err := p.fs.ReadDir(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", basePath, err)
	}

	var bpmnFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".bpmn") {
			bpmnFiles = append(bpmnFiles, p.fs.Join(basePath, entry.Name()))
		}
	}
	return bpmnFiles, nil
}

// extractComponentID extracts the component ID from annotation text.
// Expected format: "ID: ComponentName" or "ID:ComponentName"
// Returns empty string if no valid ID is found.
func extractComponentID(text string) string {
	// Look for "ID:" prefix (case-sensitive as per requirements)
	prefix := "ID:"
	idx := strings.Index(text, prefix)
	if idx == -1 {
		return ""
	}

	// Extract everything after "ID:" and trim whitespace
	componentID := strings.TrimSpace(text[idx+len(prefix):])

	// Handle multi-line text by taking only the first line
	if newlineIdx := strings.IndexAny(componentID, "\r\n"); newlineIdx != -1 {
		componentID = componentID[:newlineIdx]
	}

	return strings.TrimSpace(componentID)
}
