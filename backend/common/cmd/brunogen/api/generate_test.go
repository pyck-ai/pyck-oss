package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/api"
	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

const testGraphQL = `
mutation CreateFoo($input: CreateFooInput!) {
  createFoo(input: $input) {
    foo { id name }
    errors { message }
  }
}

mutation DeleteFoo($id: ID!) {
  deleteFoo(id: $id) {
    deletedID
  }
}
`

// setupDirs creates the temp directory tree expected by brunogen:
//
//	<root>/api/graph/apigen_gen.graphql
//	<root>/api/testdata/...
func setupDirs(t *testing.T) (graphqlFile, testdataDir, outDir string) {
	t.Helper()
	root := t.TempDir()
	graphDir := filepath.Join(root, "api", "graph")
	if err := os.MkdirAll(graphDir, 0o755); err != nil {
		t.Fatal(err)
	}
	graphqlFile = filepath.Join(graphDir, "apigen_gen.graphql")
	if err := os.WriteFile(graphqlFile, []byte(testGraphQL), 0o600); err != nil {
		t.Fatal(err)
	}
	testdataDir = filepath.Join(root, "api", "testdata")
	outDir = filepath.Join(root, "out")
	return
}

func cfg(graphqlFile, testdataDir, outDir string) types.Config {
	return types.Config{
		GraphQLFile:    graphqlFile,
		ServiceName:    "test",
		TestdataDir:    testdataDir,
		OutputDir:      filepath.Join(outDir, "examples"),
		OutputTestsDir: filepath.Join(outDir, "tests"),
	}
}

func writeTestYAML(t *testing.T, testdataDir, name, content string) {
	t.Helper()
	dir := filepath.Join(testdataDir, "tests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeExampleYAML(t *testing.T, testdataDir, name, content string) {
	t.Helper()
	dir := filepath.Join(testdataDir, "examples")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readStep(t *testing.T, outDir, scenarioSlug, filename string) string {
	t.Helper()
	return readStepForService(t, outDir, "test", scenarioSlug, filename)
}

func readStepForService(t *testing.T, outDir, service, scenarioSlug, filename string) string {
	t.Helper()
	path := filepath.Join(outDir, "tests", service, scenarioSlug, filename)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestScenario_NoSkip verifies that a step without skip produces no
// before-request script.
func TestScenario_NoSkip(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "no-skip.test.yaml", `
name: "No Skip"
steps:
  - name: "Create Foo"
    operation: createFoo
    vars:
      input:
        name: test
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "no-skip", "01_createfoo_gen.yml")
	if strings.Contains(content, "before-request") {
		t.Error("expected no before-request section for step without skip")
	}
	if strings.Contains(content, "skipRequest") {
		t.Error("expected no skipRequest call for step without skip")
	}
}

// TestScenario_StepHeaders verifies that a step's `headers:` map renders
// into a `headers:` block nested under `graphql:` in the generated Bruno
// YAML, sorted by name for deterministic output.
//
// Why nested under graphql, not top-level: Bruno's YAML parser
// (@usebruno/filestore parseGraphQLRequest) reads request headers from
// graphql.headers — a top-level headers: key is silently ignored.
func TestScenario_StepHeaders(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "with-headers.test.yaml", `
name: "With Headers"
steps:
  - name: "Create Foo"
    operation: createFoo
    headers:
      Idempotency-Key: "demo-bruno-key-001"
      X-Custom-Trace: "trace-abc"
    vars:
      input:
        name: test
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "with-headers", "01_createfoo_gen.yml")
	if !strings.Contains(content, "\n  headers:\n") {
		t.Errorf("expected a headers: block nested under graphql:; got:\n%s", content)
	}
	// Bruno's OpenCollection YAML format is a list of {name, value} entries.
	// A bare map (Idempotency-Key: "...") or a top-level headers: block is
	// silently ignored by Bruno.
	if !strings.Contains(content, "- name: Idempotency-Key\n      value: \"demo-bruno-key-001\"") {
		t.Errorf("expected Idempotency-Key list-entry; got:\n%s", content)
	}
	if !strings.Contains(content, "- name: X-Custom-Trace\n      value: \"trace-abc\"") {
		t.Errorf("expected X-Custom-Trace list-entry; got:\n%s", content)
	}
	// Sorted output: Idempotency-Key sorts before X-Custom-Trace.
	idx := strings.Index(content, "name: Idempotency-Key")
	jdx := strings.Index(content, "name: X-Custom-Trace")
	if idx < 0 || jdx < 0 || idx > jdx {
		t.Errorf("expected headers to be name-sorted; got order Idempotency-Key=%d X-Custom-Trace=%d", idx, jdx)
	}
}

// TestScenario_NoHeaders confirms a step with no headers emits no
// headers: block at all (preserving pre-feature output for the common case).
func TestScenario_NoHeaders(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "no-headers.test.yaml", `
name: "No Headers"
steps:
  - name: "Create Foo"
    operation: createFoo
    vars:
      input:
        name: test
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "no-headers", "01_createfoo_gen.yml")
	if strings.Contains(content, "headers:") {
		t.Errorf("expected no headers: block when step has no headers; got:\n%s", content)
	}
}

// TestScenario_SkipSameStepRef verifies that skip with a same-step ref
// generates a before-request try/expect/catch block.
func TestScenario_SkipSameStepRef(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "skip-ref.test.yaml", `
name: "Skip Ref"
steps:
  - name: "Delete Foo"
    operation: deleteFoo
    skip:
      - msg: "Precondition: no errors"
        assertions:
          - ref: res.body.errors
            exists: true
    vars:
      id: {$fake: uuid}
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "skip-ref", "01_deletefoo_gen.yml")
	if !strings.Contains(content, "type: before-request") {
		t.Error("expected before-request section")
	}
	if !strings.Contains(content, "bru.runner.skipRequest()") {
		t.Error("expected bru.runner.skipRequest()")
	}
	if !strings.Contains(content, "try {") {
		t.Error("expected try block")
	}
	if !strings.Contains(content, "} catch(e) {}") {
		t.Error("expected catch block")
	}
}

// TestScenario_SkipCrossStepRef verifies that a cross-step ref in skip
// generates a before-request block using bru.getVar, and that the source
// step emits the corresponding bru.setVar.
func TestScenario_SkipCrossStepRef(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "skip-cross.test.yaml", `
name: "Skip Cross Step"
steps:
  - id: createFoo
    name: "Create Foo"
    operation: createFoo
    vars:
      input:
        name: {$fake: username}

  - name: "Delete Foo"
    operation: deleteFoo
    skip:
      - msg: "Precondition: create had errors"
        assertions:
          - ref: "res[createFoo].body.errors"
            exists: true
    vars:
      id:
        $ref: "res[createFoo].body.data.createFoo.foo.id"
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	// delete step: before-request with bru.getVar as the subject
	deleteContent := readStep(t, outDir, "skip-cross-step", "02_deletefoo_gen.yml")
	if !strings.Contains(deleteContent, "type: before-request") {
		t.Error("expected before-request section in delete step")
	}
	if !strings.Contains(deleteContent, "bru.getVar(") {
		t.Error("expected bru.getVar() in skip condition (cross-step ref)")
	}
	if !strings.Contains(deleteContent, "bru.runner.skipRequest()") {
		t.Error("expected bru.runner.skipRequest() in skip script")
	}

	// create step: must emit bru.setVar for both the skip ref and the vars ref
	createContent := readStep(t, outDir, "skip-cross-step", "01_createfoo_gen.yml")
	if !strings.Contains(createContent, "bru.setVar(") {
		t.Error("expected bru.setVar in create step (to populate the skip condition and vars)")
	}
}

// TestScenario_NonceVariable verifies that {$fake: nonce} in vars renders as
// a static "{unix_timestamp}-{seq}" string baked in at code-generation time.
// The nonce must not produce a per-step before-request script.
func TestScenario_NonceVariable(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "nonce-flow.test.yaml", `
name: "Nonce Flow"
steps:
  - name: "Create Foo"
    operation: createFoo
    vars:
      input:
        name: {$fake: "{{productname}}-{{nonce}}"}
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "nonce-flow", "01_createfoo_gen.yml")
	if strings.Contains(content, "bru_nonce_ts") {
		t.Error("nonce must no longer produce {{bru_nonce_ts}} placeholder")
	}
	// nonce produces a static "{unix_timestamp}-{N}" string — no runtime scripts needed
	if strings.Contains(content, "type: before-request") {
		t.Error("nonce alone must not produce a before-request block")
	}
}

// setupBackendDirs creates a fake backend directory with two service schemas:
//
//	<root>/backend/myservice/api/graph/apigen_gen.graphql  (CreateFoo, DeleteFoo)
//	<root>/backend/other/api/graph/apigen_gen.graphql      (CreateBar)
//
// Returns (backendDir, testdataDir, outDir).
func setupBackendDirs(t *testing.T) (backendDir, testdataDir, outDir string) {
	t.Helper()
	root := t.TempDir()
	backendDir = filepath.Join(root, "backend")

	writeSchema := func(service, content string) {
		dir := filepath.Join(backendDir, service, "api", "graph")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "apigen_gen.graphql"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSchema("myservice", testGraphQL) // CreateFoo, DeleteFoo
	writeSchema("other", `
mutation CreateBar($input: CreateBarInput!) {
  createBar(input: $input) {
    bar { id }
  }
}
`)
	testdataDir = filepath.Join(backendDir, "myservice", "api", "testdata")
	outDir = filepath.Join(root, "out")
	return
}

// TestScenario_QualifiedOps verifies that GenerateScenarios resolves
// fully-qualified "service.operationName" references when BackendDir is set.
func TestScenario_QualifiedOps(t *testing.T) {
	t.Parallel()
	backendDir, testdataDir, outDir := setupBackendDirs(t)
	writeTestYAML(t, testdataDir, "cross.test.yaml", `
name: "Cross Service"
steps:
  - name: "Create Bar"
    operation: other.createBar
    vars:
      input:
        name: test
  - id: createFoo
    name: "Create Foo"
    operation: myservice.createFoo
    vars:
      input:
        name: {$fake: username}
`)
	cfg := types.Config{
		ServiceName:    "myservice",
		BackendDir:     backendDir,
		TestdataDir:    testdataDir,
		OutputTestsDir: filepath.Join(outDir, "tests"),
	}
	if err := api.GenerateScenarios(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	readStepForService(t, outDir, "myservice", "cross-service", "01_createbar_gen.yml")
	readStepForService(t, outDir, "myservice", "cross-service", "02_createfoo_gen.yml")
}

// TestScenario_UnqualifiedOpWithBackendDir verifies that an unqualified
// operation name is rejected when BackendDir is set.
func TestScenario_UnqualifiedOpWithBackendDir(t *testing.T) {
	t.Parallel()
	backendDir, testdataDir, outDir := setupBackendDirs(t)
	writeTestYAML(t, testdataDir, "bad.test.yaml", `
name: "Bad"
steps:
  - operation: createFoo
`)
	cfg := types.Config{
		ServiceName:    "myservice",
		BackendDir:     backendDir,
		TestdataDir:    testdataDir,
		OutputTestsDir: filepath.Join(outDir, "tests"),
	}
	err := api.GenerateScenarios(cfg)
	if err == nil {
		t.Fatal("expected error for unqualified operation, got nil")
	}
	if !strings.Contains(err.Error(), "fully qualified") {
		t.Errorf("expected 'fully qualified' in error, got: %v", err)
	}
}

// TestScenario_NoncePlusSkip verifies that having both nonce and skip
// produces exactly one before-request block (for skip only; nonce is static).
func TestScenario_NoncePlusSkip(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "nonce-skip.test.yaml", `
name: "Nonce Skip"
steps:
  - name: "Create Foo"
    operation: createFoo
    skip:
      - msg: "Always"
        assertions:
          - ref: res.status
            exists: true
    vars:
      input:
        name: {$fake: "{{productname}}-{{nonce}}"}
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "nonce-skip", "01_createfoo_gen.yml")
	count := strings.Count(content, "type: before-request")
	if count != 1 {
		t.Errorf("expected exactly 1 before-request block, got %d", count)
	}
	if !strings.Contains(content, "bru.runner.skipRequest()") {
		t.Error("expected skip logic in before-request")
	}
	if strings.Contains(content, "bru_nonce_ts") {
		t.Error("nonce must no longer produce {{bru_nonce_ts}} placeholder")
	}
}

// TestScenario_NoSkipNoNonce verifies that a step with neither skip nor nonce
// produces no before-request block (regression guard).
func TestScenario_NoSkipNoNonce(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeTestYAML(t, testdataDir, "plain.test.yaml", `
name: "Plain"
steps:
  - name: "Create Foo"
    operation: createFoo
    vars:
      input:
        name: {$fake: username}
`)
	if err := api.GenerateScenarios(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	content := readStep(t, outDir, "plain", "01_createfoo_gen.yml")
	if strings.Contains(content, "before-request") {
		t.Error("expected no before-request section without skip or nonce")
	}
}

// TestExample_Skip verifies that an example fixture with skip generates
// a before-request script.
func TestExample_Skip(t *testing.T) {
	t.Parallel()
	graphqlFile, testdataDir, outDir := setupDirs(t)
	writeExampleYAML(t, testdataDir, "createfoo.example.yaml", `
name: "Create Foo"
operation: createFoo
skip:
  - msg: "Always skip this example"
    assertions:
      - ref: res.status
        exists: true
vars:
  input:
    name: {$fake: username}
`)
	if err := api.GenerateExamples(cfg(graphqlFile, testdataDir, outDir)); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(outDir, "examples", "test"))
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(outDir, "examples", "test", e.Name(), "01_createfoo_gen.yml"))
		if err != nil {
			continue
		}
		content = string(b)
		break
	}
	if content == "" {
		t.Fatal("could not find generated example file")
	}
	if !strings.Contains(content, "type: before-request") {
		t.Error("expected before-request section in example with skip")
	}
	if !strings.Contains(content, "bru.runner.skipRequest()") {
		t.Error("expected bru.runner.skipRequest() in example skip script")
	}
}
