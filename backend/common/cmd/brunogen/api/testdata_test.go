package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/api"
)

// writeTempYAML writes content to a temp file and returns its path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// ── LoadExampleScenario ───────────────────────────────────────────────────────

func TestLoadExampleScenario_Valid(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Create Foo"
operation: createFoo
description: "Creates a foo."
vars:
  input:
    name: bar
expect:
  - msg: "Status is 200"
    assertions:
      - ref: res.status
        equal: 200
  - msg: "No errors"
    assertions:
      - ref: res.body.errors
        nil: true
`)
	s, err := api.LoadExampleScenario(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "Create Foo" {
		t.Errorf("Name = %q, want %q", s.Name, "Create Foo")
	}
	if s.Operation != "createFoo" {
		t.Errorf("Operation = %q, want %q", s.Operation, "createFoo")
	}
}

func TestLoadExampleScenario_MissingName(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
operation: createFoo
`)
	_, err := api.LoadExampleScenario(path)
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Errorf("expected schema validation error, got: %v", err)
	}
}

func TestLoadExampleScenario_MissingOperation(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Create Foo"
`)
	_, err := api.LoadExampleScenario(path)
	if err == nil {
		t.Fatal("expected error for missing operation, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Errorf("expected schema validation error, got: %v", err)
	}
}

func TestLoadExampleScenario_UnknownField(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Create Foo"
operation: createFoo
unknownField: oops
`)
	_, err := api.LoadExampleScenario(path)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoadExampleScenario_AssertionMissingRef(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Create Foo"
operation: createFoo
expect:
  - msg: "Status is 200"
    assertions:
      - equal: 200
`)
	_, err := api.LoadExampleScenario(path)
	if err == nil {
		t.Fatal("expected error for assertion missing ref, got nil")
	}
}

func TestLoadExampleScenario_AssertionNoTestKey(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Create Foo"
operation: createFoo
expect:
  - msg: "Status is 200"
    assertions:
      - ref: res.status
`)
	_, err := api.LoadExampleScenario(path)
	if err == nil {
		t.Fatal("expected error for assertion with no test key, got nil")
	}
}

func TestLoadExampleScenario_AssertionMultipleTestKeys(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Create Foo"
operation: createFoo
expect:
  - msg: "Status is 200"
    assertions:
      - ref: res.status
        equal: 200
        notEqual: 404
`)
	_, err := api.LoadExampleScenario(path)
	if err == nil {
		t.Fatal("expected error for assertion with multiple test keys, got nil")
	}
}

// ── LoadTestScenario ──────────────────────────────────────────────────────────

func TestLoadTestScenario_Valid(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Item lifecycle"
description: "Creates and deletes a foo."
steps:
  - id: createFoo
    name: "Create Foo"
    operation: createFoo
    vars:
      input:
        name: bar
    tests:
      - msg: "Status is 200"
        assertions:
          - ref: res.status
            equal: 200
  - name: "Delete Foo"
    operation: deleteFoo
    vars:
      id: abc
    skip:
      - msg: "Precondition: create succeeded"
        assertions:
          - ref: res[createFoo].body.errors
            exists: true
`)
	s, err := api.LoadTestScenario(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "Item lifecycle" {
		t.Errorf("Name = %q, want %q", s.Name, "Item lifecycle")
	}
	if len(s.Steps) != 2 {
		t.Errorf("Steps count = %d, want 2", len(s.Steps))
	}
}

func TestLoadTestScenario_MissingName(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
steps:
  - operation: createFoo
`)
	_, err := api.LoadTestScenario(path)
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Errorf("expected schema validation error, got: %v", err)
	}
}

func TestLoadTestScenario_MissingSteps(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Lifecycle"
`)
	_, err := api.LoadTestScenario(path)
	if err == nil {
		t.Fatal("expected error for missing steps, got nil")
	}
}

func TestLoadTestScenario_EmptySteps(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Lifecycle"
steps: []
`)
	_, err := api.LoadTestScenario(path)
	if err == nil {
		t.Fatal("expected error for empty steps array, got nil")
	}
}

func TestLoadTestScenario_StepMissingOperation(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Lifecycle"
steps:
  - name: "Create Foo"
`)
	_, err := api.LoadTestScenario(path)
	if err == nil {
		t.Fatal("expected error for step missing operation, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Errorf("expected schema validation error, got: %v", err)
	}
}

func TestLoadTestScenario_StepUnknownField(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, `
name: "Lifecycle"
steps:
  - operation: createFoo
    bogus: true
`)
	_, err := api.LoadTestScenario(path)
	if err == nil {
		t.Fatal("expected error for unknown step field, got nil")
	}
}

// ── ParseQualifiedOperation ───────────────────────────────────────────────────

func TestParseQualifiedOperation_Qualified(t *testing.T) {
	t.Parallel()
	service, op := api.ParseQualifiedOperation("management.createDataType")
	if service != "management" {
		t.Errorf("service = %q, want %q", service, "management")
	}
	if op != "createDataType" {
		t.Errorf("op = %q, want %q", op, "createDataType")
	}
}

func TestParseQualifiedOperation_Unqualified(t *testing.T) {
	t.Parallel()
	service, op := api.ParseQualifiedOperation("createDataType")
	if service != "" {
		t.Errorf("service = %q, want empty", service)
	}
	if op != "createDataType" {
		t.Errorf("op = %q, want %q", op, "createDataType")
	}
}

// ── BuildExampleIndex ─────────────────────────────────────────────────────────

func TestBuildExampleIndex_Valid(t *testing.T) {
	t.Parallel()
	td := t.TempDir()
	dir := filepath.Join(td, "examples")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "one.example.yaml"), []byte(`
name: "Create Foo"
operation: createFoo
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.example.yaml"), []byte(`
name: "Delete Foo"
operation: deleteFoo
`), 0o600); err != nil {
		t.Fatal(err)
	}

	idx, err := api.BuildExampleIndex(td)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := idx["createfoo"]; !ok {
		t.Error("expected createfoo in index")
	}
	if _, ok := idx["deletefoo"]; !ok {
		t.Error("expected deletefoo in index")
	}
}

func TestBuildExampleIndex_MissingOperation(t *testing.T) {
	t.Parallel()
	td := t.TempDir()
	dir := filepath.Join(td, "examples")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.example.yaml"), []byte(`
name: "Create Foo"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := api.BuildExampleIndex(td)
	if err == nil {
		t.Fatal("expected error for missing operation field, got nil")
	}
}

func TestBuildExampleIndex_EmptyDir(t *testing.T) {
	t.Parallel()
	td := t.TempDir()
	idx, err := api.BuildExampleIndex(td)
	if err != nil {
		t.Fatalf("unexpected error for missing examples dir: %v", err)
	}
	if len(idx) != 0 {
		t.Errorf("expected empty index, got %v", idx)
	}
}

// ── Generated output ──────────────────────────────────────────────────────────

// TestExample_SeqPrefix verifies that generated example files carry the CRUD
// sequence prefix (e.g. 01_ for create, 04_ for delete).
func TestExample_SeqPrefix(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	if err := api.GenerateExamples(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	serviceDir := filepath.Join(outDir, "examples", "test")
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub, _ := os.ReadDir(filepath.Join(serviceDir, e.Name()))
		for _, f := range sub {
			found[f.Name()] = true
		}
	}
	if !found["01_createfoo_gen.yml"] {
		t.Error("expected 01_createfoo_gen.yml (create → seq 1)")
	}
	if !found["04_deletefoo_gen.yml"] {
		t.Error("expected 04_deletefoo_gen.yml (delete → seq 4)")
	}
}

// TestExample_OperationFieldMapping verifies that the operation: field (not
// the filename) is used to match a fixture to its GraphQL operation.
func TestExample_OperationFieldMapping(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	// Filename deliberately does not match the operation name.
	writeExampleYAML(t, testdataDir, "arbitrary-name.example.yaml", `
name: "Create Foo"
operation: createFoo
description: "from fixture"
vars:
  input:
    name: bar
expect:
  - msg: "Status is 200"
    assertions:
      - ref: res.status
        equal: 200
`)
	if err := api.GenerateExamples(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	serviceDir := filepath.Join(outDir, "examples", "test")
	entries, _ := os.ReadDir(serviceDir)
	var content string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(serviceDir, e.Name(), "01_createfoo_gen.yml"))
		if err == nil {
			content = string(b)
			break
		}
	}
	if content == "" {
		t.Fatal("could not find generated 01_createfoo_gen.yml")
	}
	if !strings.Contains(content, "from fixture") {
		t.Error("expected fixture description in generated file")
	}
}

// TestExample_PlaceholderWarning verifies that a $placeholder var triggers
// the ACTION REQUIRED warning block in the generated query.
func TestExample_PlaceholderWarning(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeExampleYAML(t, testdataDir, "deletefoo.example.yaml", `
name: "Delete Foo"
operation: deleteFoo
vars:
  id: {$placeholder: "foo ID"}
expect:
  - msg: "Status is 200"
    assertions:
      - ref: res.status
        equal: 200
`)
	if err := api.GenerateExamples(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	serviceDir := filepath.Join(outDir, "examples", "test")
	entries, _ := os.ReadDir(serviceDir)
	var content string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(serviceDir, e.Name(), "04_deletefoo_gen.yml"))
		if err == nil {
			content = string(b)
			break
		}
	}
	if content == "" {
		t.Fatal("could not find generated 04_deletefoo_gen.yml")
	}
	if !strings.Contains(content, "ACTION REQUIRED") {
		t.Error("expected ACTION REQUIRED warning for $placeholder vars")
	}
	if !strings.Contains(content, "Variables tab") {
		t.Error("expected 'Variables tab' reference in warning")
	}
}

// TestExample_SourceComment verifies that the generated file contains a
// "# Source: ..." comment pointing at the fixture file.
func TestExample_SourceComment(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeExampleYAML(t, testdataDir, "createfoo.example.yaml", `
name: "Create Foo"
operation: createFoo
expect:
  - msg: "Status is 200"
    assertions:
      - ref: res.status
        equal: 200
`)
	if err := api.GenerateExamples(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	serviceDir := filepath.Join(outDir, "examples", "test")
	entries, _ := os.ReadDir(serviceDir)
	var content string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(serviceDir, e.Name(), "01_createfoo_gen.yml"))
		if err == nil {
			content = string(b)
			break
		}
	}
	if content == "" {
		t.Fatal("could not find generated 01_createfoo_gen.yml")
	}
	if !strings.Contains(content, "# Source:") {
		t.Error("expected '# Source:' comment in generated file")
	}
	if !strings.Contains(content, "createfoo.example.yaml") {
		t.Error("expected fixture filename in # Source comment")
	}
}

// TestScenario_SourceComment verifies that the generated test step contains a
// "# Source: ..." comment pointing at the test fixture file.
func TestScenario_SourceComment(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "my-flow.test.yaml", `
name: "My Flow"
steps:
  - operation: createFoo
    vars:
      input:
        name: bar
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "my-flow", "01_createfoo_gen.yml")
	if !strings.Contains(content, "# Source:") {
		t.Error("expected '# Source:' comment in generated test step")
	}
	if !strings.Contains(content, "my-flow.test.yaml") {
		t.Error("expected fixture filename in # Source comment")
	}
}

// TestExample_NoPlaceholderWarning verifies that a fixture without $placeholder
// does NOT emit the warning block.
func TestExample_NoPlaceholderWarning(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeExampleYAML(t, testdataDir, "createfoo.example.yaml", `
name: "Create Foo"
operation: createFoo
vars:
  input:
    name: hardcoded
expect:
  - msg: "Status is 200"
    assertions:
      - ref: res.status
        equal: 200
`)
	if err := api.GenerateExamples(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	serviceDir := filepath.Join(outDir, "examples", "test")
	entries, _ := os.ReadDir(serviceDir)
	var content string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(serviceDir, e.Name(), "01_createfoo_gen.yml"))
		if err == nil {
			content = string(b)
			break
		}
	}
	if content == "" {
		t.Fatal("could not find generated 01_createfoo_gen.yml")
	}
	if strings.Contains(content, "ACTION REQUIRED") {
		t.Error("unexpected ACTION REQUIRED warning for non-placeholder vars")
	}
}
