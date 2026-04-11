package api

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	_ "embed"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

//go:embed schemas/example.schema.json
var exampleSchemaJSON []byte

//go:embed schemas/test.schema.json
var testSchemaJSON []byte

var (
	exampleSchema = mustCompileSchema("example.schema.json", exampleSchemaJSON)
	testSchema    = mustCompileSchema("test.schema.json", testSchemaJSON)
)

func mustCompileSchema(id string, data []byte) *jsonschema.Schema {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(id, bytes.NewReader(data)); err != nil {
		panic("brunogen: failed to add schema resource " + id + ": " + err.Error())
	}
	schema, err := compiler.Compile(id)
	if err != nil {
		panic("brunogen: failed to compile schema " + id + ": " + err.Error())
	}
	return schema
}

// validateYAML validates raw YAML bytes against a compiled JSON schema.
func validateYAML(filePath string, data []byte, schema *jsonschema.Schema) error {
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to parse YAML in %s: %w", filePath, err)
	}
	if err := schema.Validate(raw); err != nil {
		return fmt.Errorf("schema validation failed for %s: %w", filePath, err)
	}
	return nil
}

// TestDataDir returns the testdata directory derived from the GraphQL file path.
// e.g. backend/<service>/api/graph/apigen_gen.graphql → backend/<service>/api/testdata/
func TestDataDir(graphqlFile string) string {
	abs, err := filepath.Abs(graphqlFile)
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(abs)), "testdata")
}

// BuildExampleIndex scans testdataDir/examples/ and returns a map from
// lowercase operation name to file path. Every example file must declare an
// explicit `operation:` field; files missing it cause a hard error.
func BuildExampleIndex(testdataDir string) (map[string]string, error) {
	dir := filepath.Join(testdataDir, "examples")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read examples directory %s: %w", dir, err)
	}
	index := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".example.yaml") && !strings.HasSuffix(name, ".example.yml") {
			continue
		}
		path := filepath.Join(dir, name)
		scenario, err := LoadExampleScenario(path)
		if err != nil {
			return nil, err
		}
		if scenario.Operation == "" {
			return nil, fmt.Errorf("example file %s: %w", path, types.ErrMissingOperation)
		}
		index[strings.ToLower(scenario.Operation)] = path
	}
	return index, nil
}

// FindAllTestFiles returns all .test.yaml/.test.yml files in testdataDir/tests/.
func FindAllTestFiles(testdataDir string) ([]string, error) {
	testsDir := filepath.Join(testdataDir, "tests")
	entries, err := os.ReadDir(testsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read tests directory %s: %w", testsDir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".test.yaml") || strings.HasSuffix(name, ".test.yml") {
			files = append(files, filepath.Join(testsDir, name))
		}
	}
	return files, nil
}

// LoadExampleScenario parses and validates a single-document .example.yaml file.
func LoadExampleScenario(filePath string) (*types.ExampleScenario, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read example file: %w", err)
	}
	if err := validateYAML(filePath, data, exampleSchema); err != nil {
		return nil, err
	}
	var s types.ExampleScenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to decode YAML in %s: %w", filePath, err)
	}
	return &s, nil
}

// LoadTestScenario parses and validates a single-document .test.yaml file.
func LoadTestScenario(filePath string) (*types.TestScenario, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read test file: %w", err)
	}
	if err := validateYAML(filePath, data, testSchema); err != nil {
		return nil, err
	}
	var s types.TestScenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to decode YAML in %s: %w", filePath, err)
	}
	return &s, nil
}
