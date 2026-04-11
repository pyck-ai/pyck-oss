package instructions // nolint:testpackage // Need access to unexported functions generateWorkflowMetadata and generateMainFile

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateWorkflowMetadata_StructBasedWorkflows(t *testing.T) {
	// Create a temporary directory with test Go files
	tmpDir := t.TempDir()

	// Create go.mod
	goModContent := `module testproject

go 1.23

require (
	github.com/pyck-ai/pyck/backend/workflowsdk v0.0.0
	go.temporal.io/sdk v1.25.0
)
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a struct-based workflow file
	structWorkflowContent := `package workflows

import (
	"go.temporal.io/sdk/workflow"
	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

// ApprovalWorkflow is a struct-based workflow
type ApprovalWorkflow struct{}

func (ApprovalWorkflow) WorkflowName() string {
	return "Approval"
}

func (ApprovalWorkflow) WorkflowSignals() []workflowsdk.Signal {
	return []workflowsdk.Signal{
		workflowsdk.NewStartSignal().
			WithTemporalSignal("ApprovalStarted").
			WithTopic("approval.started"),
	}
}

func (ApprovalWorkflow) Workflow(ctx workflow.Context, data map[string]any) error {
	return nil
}

func (ApprovalWorkflow) WorkflowActivities() workflowsdk.Activities {
	return workflowsdk.Activities{}
}
`
	workflowsDir := filepath.Join(tmpDir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "approval.go"), []byte(structWorkflowContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Test workflow metadata generation
	metadata, err := generateWorkflowMetadata(tmpDir)
	if err != nil {
		t.Fatalf("generateWorkflowMetadata() error = %v", err)
	}

	if len(metadata.Workflows) == 0 {
		t.Fatal("Expected at least one workflow to be detected")
	}

	found := false
	for _, wf := range metadata.Workflows {
		if wf.Identifier == "ApprovalWorkflow" {
			found = true
			if wf.HasFn {
				t.Errorf("Expected HasFn=false for struct-based workflow, got true")
			}
			break
		}
	}

	if !found {
		t.Error("ApprovalWorkflow not found in detected workflows")
	}
}

func TestGenerateWorkflowMetadata_MissingRequiredMethods(t *testing.T) {
	// Create a temporary directory with test Go files
	tmpDir := t.TempDir()

	// Create go.mod
	goModContent := `module testproject

go 1.23

require (
	github.com/pyck-ai/pyck/backend/workflowsdk v0.0.0
	go.temporal.io/sdk v1.25.0
)
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a struct WITHOUT required methods (should NOT be detected)
	invalidWorkflowContent := `package workflows

import "go.temporal.io/sdk/workflow"

// InvalidWorkflow missing required methods
type InvalidWorkflow struct{}

func (InvalidWorkflow) WorkflowName() string {
	return "Invalid"
}

// Missing WorkflowSignals() and Workflow() methods
`
	workflowsDir := filepath.Join(tmpDir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "invalid.go"), []byte(invalidWorkflowContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Test workflow metadata generation
	_, err := generateWorkflowMetadata(tmpDir)

	// We expect an error since no valid workflows are found
	if err == nil {
		t.Error("Expected error for project with no valid workflows")
	}
}

func TestGenerateWorkflowMetadata_MultipleStructWorkflows(t *testing.T) {
	// Create a temporary directory with test Go files
	tmpDir := t.TempDir()

	// Create go.mod
	goModContent := `module testproject

go 1.23

require (
	github.com/pyck-ai/pyck/backend/workflowsdk v0.0.0
	go.temporal.io/sdk v1.25.0
)
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create multiple struct-based workflows
	workflowsContent := `package workflows

import (
	"go.temporal.io/sdk/workflow"
	"github.com/pyck-ai/pyck/backend/workflowsdk"
)

// ApprovalWorkflow is a struct-based workflow
type ApprovalWorkflow struct{}

func (ApprovalWorkflow) WorkflowName() string {
	return "Approval"
}

func (ApprovalWorkflow) WorkflowSignals() []workflowsdk.Signal {
	return []workflowsdk.Signal{}
}

func (ApprovalWorkflow) Workflow(ctx workflow.Context, data map[string]any) error {
	return nil
}

// OrderWorkflow is another struct-based workflow
type OrderWorkflow struct{}

func (OrderWorkflow) WorkflowName() string {
	return "Order"
}

func (OrderWorkflow) WorkflowSignals() []workflowsdk.Signal {
	return []workflowsdk.Signal{}
}

func (OrderWorkflow) Workflow(ctx workflow.Context, data map[string]any) error {
	return nil
}
`
	workflowsDir := filepath.Join(tmpDir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "workflows.go"), []byte(workflowsContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Test workflow metadata generation
	metadata, err := generateWorkflowMetadata(tmpDir)
	if err != nil {
		t.Fatalf("generateWorkflowMetadata() error = %v", err)
	}

	if len(metadata.Workflows) != 2 {
		t.Fatalf("Expected 2 workflows, got %d", len(metadata.Workflows))
	}

	foundApproval := false
	foundOrder := false

	for _, wf := range metadata.Workflows {
		switch wf.Identifier {
		case "ApprovalWorkflow":
			foundApproval = true
		case "OrderWorkflow":
			foundOrder = true
		}
	}

	if !foundApproval {
		t.Error("ApprovalWorkflow not found")
	}
	if !foundOrder {
		t.Error("OrderWorkflow not found")
	}
}

func TestGenerateMainFile_StructBasedWorkflow(t *testing.T) {
	// Create temporary directory with go.mod
	tmpDir := t.TempDir()

	goModContent := `module github.com/test/testproject

go 1.23

require (
	github.com/pyck-ai/pyck/backend/workflowsdk v0.0.0
	go.temporal.io/sdk v1.25.0
)
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Setup test metadata
	metadata := WorkflowMetadataResult{
		WorkflowsdkImportPath: "github.com/pyck-ai/pyck/backend/workflowsdk",
		Workflows: []WorkflowDescriptorMeta{
			{
				Import:     ImportSpec{Alias: "testworkflows", Path: "testpackage/workflows"},
				Identifier: "TestWorkflow",
				HasFn:      false, // Struct-based workflow
			},
		},
		Activities: []ActivityDescriptorMeta{
			{
				Import:     ImportSpec{Alias: "testactivities", Path: "testpackage/activities"},
				Identifier: "TestActivity",
			},
		},
	}

	// Generate main.go
	err := generateMainFile(tmpDir, metadata)
	if err != nil {
		t.Fatalf("generateMainFile failed: %v", err)
	}

	// Read generated file
	mainPath := filepath.Join(tmpDir, "main.go")
	generated, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("Failed to read generated main.go: %v", err)
	}

	// Parse generated code to validate syntax
	fset := token.NewFileSet()
	_, err = parser.ParseFile(fset, "main.go", generated, parser.AllErrors)
	if err != nil {
		t.Fatalf("Generated code has syntax errors: %v", err)
	}

	// Validate content
	content := string(generated)

	// Check imports
	if !strings.Contains(content, `"reflect"`) {
		t.Error("Generated code missing reflect import")
	}
	if !strings.Contains(content, `workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"`) {
		t.Error("Generated code missing workflowsdk import")
	}
	if !strings.Contains(content, `testworkflows "testpackage/workflows"`) {
		t.Error("Generated code missing workflow package import")
	}

	// Check struct instantiation
	if !strings.Contains(content, "testworkflows.TestWorkflow{}") {
		t.Error("Generated code missing struct instantiation")
	}

	// Check reflection-based registration
	if !strings.Contains(content, "reflect.ValueOf") {
		t.Error("Generated code missing reflection-based registration")
	}
	if !strings.Contains(content, `MethodByName("Workflow")`) {
		t.Error("Generated code missing Workflow method extraction")
	}

	// Ensure no legacy descriptor code
	if strings.Contains(content, "WorkflowWithFn") {
		t.Error("Generated code contains legacy descriptor-based code (WorkflowWithFn)")
	}
	if strings.Contains(content, "WithSignals") && !strings.Contains(content, "RegisterWorkflowWithSignalsInput") {
		t.Error("Generated code contains legacy WithSignals interface usage")
	}
}

func TestGenerateMainFile_MultipleWorkflows(t *testing.T) {
	// Create temporary directory with go.mod
	tmpDir := t.TempDir()

	goModContent := `module github.com/test/multiworkflow

go 1.23

require (
	github.com/pyck-ai/pyck/backend/workflowsdk v0.0.0
	go.temporal.io/sdk v1.25.0
)
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Setup test data with multiple workflows
	metadata := WorkflowMetadataResult{
		WorkflowsdkImportPath: "github.com/pyck-ai/pyck/backend/workflowsdk",
		Workflows: []WorkflowDescriptorMeta{
			{
				Import:     ImportSpec{Alias: "workflow1", Path: "testpackage/workflow1"},
				Identifier: "WorkflowOne",
				HasFn:      false,
			},
			{
				Import:     ImportSpec{Alias: "workflow2", Path: "testpackage/workflow2"},
				Identifier: "WorkflowTwo",
				HasFn:      false,
			},
		},
		Activities: []ActivityDescriptorMeta{
			{
				Import:     ImportSpec{Alias: "activities", Path: "testpackage/activities"},
				Identifier: "ActivityOne",
			},
		},
	}

	// Generate main.go
	err := generateMainFile(tmpDir, metadata)
	if err != nil {
		t.Fatalf("generateMainFile failed: %v", err)
	}

	// Read generated file
	mainPath := filepath.Join(tmpDir, "main.go")
	generated, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("Failed to read generated main.go: %v", err)
	}

	// Parse generated code to validate syntax
	fset := token.NewFileSet()
	_, err = parser.ParseFile(fset, "main.go", generated, parser.AllErrors)
	if err != nil {
		t.Fatalf("Generated code has syntax errors: %v", err)
	}

	// Validate content
	content := string(generated)

	// Check both workflows are instantiated
	if !strings.Contains(content, "workflow1.WorkflowOne{}") {
		t.Error("Generated code missing WorkflowOne instantiation")
	}
	if !strings.Contains(content, "workflow2.WorkflowTwo{}") {
		t.Error("Generated code missing WorkflowTwo instantiation")
	}

	// Check workflow list structure
	if !strings.Contains(content, "var workflowsList = []workflowsdk.Workflow{") {
		t.Error("Generated code missing workflows list")
	}

	// Count workflow registrations - should have 2
	reflectCount := strings.Count(content, "reflect.ValueOf")
	if reflectCount < 1 {
		t.Error("Generated code missing reflection-based workflow registration")
	}
}
