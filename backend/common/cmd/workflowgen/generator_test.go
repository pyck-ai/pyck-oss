package main

import (
	"strings"
	"testing"
)

// TestUpdateMainFile_EmptyFile tests adding imports to a file with no existing imports
func TestUpdateMainFile_EmptyFile(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	fs.files["main.go"] = []byte(`package main

func main() {
	// Empty main
}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, removed, err := gen.UpdateMainFile([]string{
		"example.com/test/workflows/workflow1",
		"example.com/test/workflows/workflow2",
	})
	if err != nil {
		t.Fatalf("UpdateMainFile failed: %v", err)
	}

	if len(added) != 2 {
		t.Errorf("Expected 2 imports added, got %d", len(added))
	}

	if len(removed) != 0 {
		t.Errorf("Expected 0 imports removed, got %d", len(removed))
	}

	content := string(fs.files["main.go"])
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow1"`) {
		t.Error("Expected workflow1 import not found")
	}
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow2"`) {
		t.Error("Expected workflow2 import not found")
	}
}

// TestUpdateMainFile_AddToExisting tests adding imports to a file with existing imports
func TestUpdateMainFile_AddToExisting(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	fs.files["main.go"] = []byte(`package main

import (
	_ "example.com/test/workflows/workflow1"
)

func main() {
	// Empty main
}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, removed, err := gen.UpdateMainFile([]string{
		"example.com/test/workflows/workflow1",
		"example.com/test/workflows/workflow2",
	})
	if err != nil {
		t.Fatalf("UpdateMainFile failed: %v", err)
	}

	if len(added) != 1 {
		t.Errorf("Expected 1 import added, got %d", len(added))
	}

	if len(removed) != 0 {
		t.Errorf("Expected 0 imports removed, got %d", len(removed))
	}

	content := string(fs.files["main.go"])
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow2"`) {
		t.Error("Expected workflow2 import not found")
	}
}

// TestUpdateMainFile_RemoveStale tests removing imports that are no longer needed
func TestUpdateMainFile_RemoveStale(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	fs.files["main.go"] = []byte(`package main

import (
	_ "example.com/test/workflows/workflow1"
	_ "example.com/test/workflows/workflow2"
	_ "example.com/test/workflows/workflow3"
)

func main() {
	// Empty main
}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, removed, err := gen.UpdateMainFile([]string{
		"example.com/test/workflows/workflow1",
	})
	if err != nil {
		t.Fatalf("UpdateMainFile failed: %v", err)
	}

	if len(added) != 0 {
		t.Errorf("Expected 0 imports added, got %d", len(added))
	}

	if len(removed) != 2 {
		t.Errorf("Expected 2 imports removed, got %d", len(removed))
	}

	content := string(fs.files["main.go"])
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow1"`) {
		t.Error("Expected workflow1 import not found")
	}
	if strings.Contains(content, "workflow2") {
		t.Error("workflow2 should have been removed")
	}
	if strings.Contains(content, "workflow3") {
		t.Error("workflow3 should have been removed")
	}
}

// TestUpdateMainFile_PreserveNonUnderscore tests that non-underscore imports are preserved
func TestUpdateMainFile_PreserveNonUnderscore(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	fs.files["main.go"] = []byte(`package main

import (
	"fmt"
	"os"
	_ "example.com/test/workflows/workflow1"
)

func main() {
	fmt.Println("Hello")
	os.Exit(0)
}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, removed, err := gen.UpdateMainFile([]string{
		"example.com/test/workflows/workflow2",
	})
	if err != nil {
		t.Fatalf("UpdateMainFile failed: %v", err)
	}

	if len(added) != 1 {
		t.Errorf("Expected 1 import added, got %d", len(added))
	}

	if len(removed) != 1 {
		t.Errorf("Expected 1 import removed, got %d", len(removed))
	}

	content := string(fs.files["main.go"])
	if !strings.Contains(content, `"fmt"`) {
		t.Error("fmt import should be preserved")
	}
	if !strings.Contains(content, `"os"`) {
		t.Error("os import should be preserved")
	}
	if strings.Contains(content, "workflow1") {
		t.Error("workflow1 should have been removed")
	}
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow2"`) {
		t.Error("Expected workflow2 import not found")
	}
}

// TestFileHasInit_WithInit tests detection of init() function
func TestFileHasInit_WithInit(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	testCases := []struct {
		name    string
		content string
	}{
		{
			name: "simple init",
			content: `package workflow1

func init() {
	// Register workflows
}
`,
		},
		{
			name: "init with body",
			content: `package workflow1

import "fmt"

func init() {
	fmt.Println("Initializing")
	registerWorkflows()
}
`,
		},
		{
			name: "multiple functions with init",
			content: `package workflow1

func setup() {}

func init() {
	setup()
}

func run() {}
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hasInit, err := gen.FileHasInit([]byte(tc.content))
			if err != nil {
				t.Fatalf("FileHasInit failed: %v", err)
			}
			if !hasInit {
				t.Error("Expected init() to be detected")
			}
		})
	}
}

// TestFileHasInit_WithoutInit tests files without init() function
func TestFileHasInit_WithoutInit(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	testCases := []struct {
		name    string
		content string
	}{
		{
			name: "empty file",
			content: `package workflow1
`,
		},
		{
			name: "only types",
			content: `package workflow1

type Workflow struct{}
`,
		},
		{
			name: "functions but no init",
			content: `package workflow1

func Run() {}
func Setup() {}
`,
		},
		{
			name: "init in string",
			content: `package workflow1

const code = "func init() {}"
`,
		},
		{
			name: "init in comment",
			content: `package workflow1

// This function is like init() but different
func setup() {}
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hasInit, err := gen.FileHasInit([]byte(tc.content))
			if err != nil {
				t.Fatalf("FileHasInit failed: %v", err)
			}
			if hasInit {
				t.Error("Expected no init() to be detected")
			}
		})
	}
}

// TestFileHasInit_EdgeCases tests edge cases in init detection
func TestFileHasInit_EdgeCases(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	testCases := []struct {
		name     string
		content  string
		hasInit  bool
		hasError bool
	}{
		{
			name: "nested init function",
			content: `package workflow1

func outer() {
	init := func() {}
	init()
}
`,
			hasInit:  false,
			hasError: false,
		},
		{
			name: "method named init",
			content: `package workflow1

type Workflow struct{}

func (w *Workflow) init() {}
`,
			hasInit:  false,
			hasError: false,
		},
		{
			name:     "invalid go syntax",
			content:  `this is not valid go code`,
			hasInit:  false,
			hasError: true,
		},
		{
			name: "init with parameters (invalid but parses)",
			content: `package workflow1

func init(x int) {}
`,
			hasInit:  true, // Go parser accepts this, though it's invalid semantically
			hasError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hasInit, err := gen.FileHasInit([]byte(tc.content))
			if tc.hasError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if hasInit != tc.hasInit {
				t.Errorf("Expected hasInit=%v, got %v", tc.hasInit, hasInit)
			}
		})
	}
}

// TestFindPackagesWithInit_Recursive tests recursive directory scanning
func TestFindPackagesWithInit_Recursive(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	// Create a directory structure with workflows
	fs.files["workflows/workflow1/workflow.go"] = []byte(`package workflow1

func init() {}
`)
	fs.files["workflows/workflow2/workflow.go"] = []byte(`package workflow2

func init() {}
`)
	fs.files["workflows/nested/deep/workflow3/workflow.go"] = []byte(`package workflow3

func init() {}
`)
	fs.files["workflows/noInit/workflow.go"] = []byte(`package noInit

func Run() {}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	packages, err := gen.FindPackagesWithInit("workflows/...")
	if err != nil {
		t.Fatalf("FindPackagesWithInit failed: %v", err)
	}

	expectedCount := 3
	if len(packages) != expectedCount {
		t.Errorf("Expected %d packages, got %d: %v", expectedCount, len(packages), packages)
	}

	expectedPackages := map[string]bool{
		"example.com/test/workflows/workflow1":             true,
		"example.com/test/workflows/workflow2":             true,
		"example.com/test/workflows/nested/deep/workflow3": true,
	}

	for _, pkg := range packages {
		if !expectedPackages[pkg] {
			t.Errorf("Unexpected package found: %s", pkg)
		}
	}
}

// TestFindPackagesWithInit_NonRecursive tests non-recursive directory scanning
func TestFindPackagesWithInit_NonRecursive(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	// Create files directly in the workflows directory (non-recursive mode checks files in the directory itself)
	fs.files["workflows/workflow.go"] = []byte(`package workflows

func init() {}
`)
	fs.files["workflows/helper.go"] = []byte(`package workflows

func Helper() {}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	packages, err := gen.FindPackagesWithInit("workflows")
	if err != nil {
		t.Fatalf("FindPackagesWithInit failed: %v", err)
	}

	// Non-recursive should find the workflows package itself if it has init
	expectedCount := 1
	if len(packages) != expectedCount {
		t.Errorf("Expected %d packages, got %d: %v", expectedCount, len(packages), packages)
	}

	if len(packages) > 0 && packages[0] != "example.com/test/workflows" {
		t.Errorf("Expected workflows package, got %s", packages[0])
	}
}

// TestFindPackagesWithInit_EmptyDirectory tests scanning empty directory
func TestFindPackagesWithInit_EmptyDirectory(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	packages, err := gen.FindPackagesWithInit("workflows/...")
	if err != nil {
		t.Fatalf("FindPackagesWithInit failed: %v", err)
	}

	if len(packages) != 0 {
		t.Errorf("Expected 0 packages in empty directory, got %d", len(packages))
	}
}

// TestRun_FullIntegration tests the complete generation workflow
func TestRun_FullIntegration(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	// Set up a realistic structure
	fs.files["main.go"] = []byte(`package main

import (
	"fmt"
	_ "example.com/test/workflows/old"
)

func main() {
	fmt.Println("Starting")
}
`)

	fs.files["workflows/workflow1/workflow.go"] = []byte(`package workflow1

func init() {
	// Register workflow1
}
`)

	fs.files["workflows/workflow2/workflow.go"] = []byte(`package workflow2

func init() {
	// Register workflow2
}
`)

	fs.files["workflows/noInit/helper.go"] = []byte(`package noInit

func Helper() {}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, removed, err := gen.Run([]string{"workflows/..."})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Should add 2 new imports
	if len(added) != 2 {
		t.Errorf("Expected 2 imports added, got %d: %v", len(added), added)
	}

	// Should remove 1 old import
	if len(removed) != 1 {
		t.Errorf("Expected 1 import removed, got %d: %v", len(removed), removed)
	}

	content := string(fs.files["main.go"])
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow1"`) {
		t.Error("Expected workflow1 import not found")
	}
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow2"`) {
		t.Error("Expected workflow2 import not found")
	}
	if strings.Contains(content, "workflows/old") {
		t.Error("Old import should have been removed")
	}
	if strings.Contains(content, "noInit") {
		t.Error("Package without init should not be imported")
	}
}

// TestRun_MultipleSearchPaths tests scanning multiple directories
func TestRun_MultipleSearchPaths(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	fs.files["main.go"] = []byte(`package main

func main() {}
`)

	fs.files["workflows/w1/workflow.go"] = []byte(`package w1

func init() {}
`)

	fs.files["activities/a1/activity.go"] = []byte(`package a1

func init() {}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, _, err := gen.Run([]string{"workflows/...", "activities/..."})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(added) != 2 {
		t.Errorf("Expected 2 imports added, got %d", len(added))
	}

	content := string(fs.files["main.go"])
	if !strings.Contains(content, `_ "example.com/test/workflows/w1"`) {
		t.Error("Expected workflows/w1 import not found")
	}
	if !strings.Contains(content, `_ "example.com/test/activities/a1"`) {
		t.Error("Expected activities/a1 import not found")
	}
}

// TestRun_NoChangesNeeded tests when imports are already correct
func TestRun_NoChangesNeeded(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	fs.files["main.go"] = []byte(`package main

import (
	_ "example.com/test/workflows/workflow1"
)

func main() {}
`)

	fs.files["workflows/workflow1/workflow.go"] = []byte(`package workflow1

func init() {}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, removed, err := gen.Run([]string{"workflows/..."})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(added) != 0 {
		t.Errorf("Expected 0 imports added, got %d", len(added))
	}

	if len(removed) != 0 {
		t.Errorf("Expected 0 imports removed, got %d", len(removed))
	}
}

// TestUpdateMainFile_MalformedFile tests error handling for invalid Go files
func TestUpdateMainFile_MalformedFile(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	fs.files["main.go"] = []byte(`this is not valid go code`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	_, _, err := gen.UpdateMainFile([]string{})
	if err == nil {
		t.Error("Expected error for malformed file")
	}
}

// TestUpdateMainFile_MissingFile tests handling of missing main file
func TestUpdateMainFile_MissingFile(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	gen := &Generator{
		fs:         fs,
		modulePath: "example.com/test",
		mainTemplate: `package main

func main() {
	// Empty
}
`,
		verbose: false,
	}

	// UpdateMainFile should create the file from template if it doesn't exist
	added, removed, err := gen.UpdateMainFile([]string{"example.com/test/workflows/workflow1"})
	if err != nil {
		t.Fatalf("UpdateMainFile should create file from template: %v", err)
	}

	if len(added) != 1 {
		t.Errorf("Expected 1 import added, got %d", len(added))
	}

	if len(removed) != 0 {
		t.Errorf("Expected 0 imports removed, got %d", len(removed))
	}

	// Verify file was created
	content, err := fs.ReadFile("main.go")
	if err != nil {
		t.Fatalf("File should have been created: %v", err)
	}

	if !strings.Contains(string(content), "workflow1") {
		t.Error("Created file should contain the import")
	}
}

// TestFindPackagesWithInit_MultipleFilesInPackage tests packages with multiple Go files
func TestFindPackagesWithInit_MultipleFilesInPackage(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()

	// Package with multiple files, init in one of them
	fs.files["workflows/workflow1/workflow.go"] = []byte(`package workflow1

func Run() {}
`)
	fs.files["workflows/workflow1/init.go"] = []byte(`package workflow1

func init() {
	// Register
}
`)
	fs.files["workflows/workflow1/helper.go"] = []byte(`package workflow1

func Helper() {}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	packages, err := gen.FindPackagesWithInit("workflows/...")
	if err != nil {
		t.Fatalf("FindPackagesWithInit failed: %v", err)
	}

	if len(packages) != 1 {
		t.Errorf("Expected 1 package, got %d", len(packages))
	}

	if packages[0] != "example.com/test/workflows/workflow1" {
		t.Errorf("Expected workflow1 package, got %s", packages[0])
	}
}

// TestUpdateMainFile_ImportGrouping tests that imports are properly grouped
func TestUpdateMainFile_ImportGrouping(t *testing.T) {
	t.Parallel()
	fs := newMemFileSystem()
	fs.files["main.go"] = []byte(`package main

import (
	"context"
	"fmt"

	"github.com/external/package"

	_ "example.com/test/workflows/workflow1"
)

func main() {
	fmt.Println(context.Background())
}
`)

	gen := &Generator{
		fs:           fs,
		modulePath:   "example.com/test",
		mainTemplate: "main.go",
		verbose:      false,
	}

	added, removed, err := gen.UpdateMainFile([]string{
		"example.com/test/workflows/workflow2",
	})
	if err != nil {
		t.Fatalf("UpdateMainFile failed: %v", err)
	}

	if len(added) != 1 || len(removed) != 1 {
		t.Errorf("Expected 1 added and 1 removed, got %d added and %d removed", len(added), len(removed))
	}

	content := string(fs.files["main.go"])

	// Verify standard library imports are preserved
	if !strings.Contains(content, `"context"`) {
		t.Error("context import should be preserved")
	}
	if !strings.Contains(content, `"fmt"`) {
		t.Error("fmt import should be preserved")
	}

	// Verify external imports are preserved
	if !strings.Contains(content, `"github.com/external/package"`) {
		t.Error("external package import should be preserved")
	}

	// Verify workflow imports are updated correctly
	if !strings.Contains(content, `_ "example.com/test/workflows/workflow2"`) {
		t.Error("workflow2 import should be added")
	}
	if strings.Contains(content, "workflow1") {
		t.Error("workflow1 import should be removed")
	}
}
