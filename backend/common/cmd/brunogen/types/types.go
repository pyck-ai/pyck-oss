// Package types contains the shared data model for brunogen: configuration,
// parsed GraphQL types, and test-fixture types.
package types

import "strings"

// Config holds all settings for a brunogen generation run.
type Config struct {
	// ServiceName is the service identifier, e.g. "inventory".
	// Auto-detected from GraphQLFile (examples) or CWD (tests) if empty.
	ServiceName string
	// GraphQLFile is the path to the apigen_gen.graphql file.
	// Used by the examples subcommand only.
	GraphQLFile string
	// BackendDir is the path to the backend directory containing all service
	// subdirectories. Used by the tests subcommand for cross-service operation
	// lookup. When set, operations in test fixtures must be fully qualified as
	// "service.operationName".
	BackendDir string
	// OutputDir is the output directory for the examples Bruno collection.
	OutputDir string
	// OutputTestsDir is the output directory for the tests Bruno collection.
	OutputTestsDir string
	// TestdataDir overrides the testdata directory path.
	// Derived from GraphQLFile (api/testdata/) if empty.
	TestdataDir string
	// Verbose enables logging of generated file paths.
	Verbose bool
	// DryRun skips writing files and logs what would be written instead.
	DryRun bool
}

// Operation represents a single GraphQL query or mutation.
type Operation struct {
	Name       string     // operation name, e.g. "createInventoryItem"
	Type       string     // "query" or "mutation"
	Content    string     // full GraphQL operation text
	Variables  []Variable // declared input variables
	ReturnType string     // primary return type inferred from the operation body
}

// Variable is a single GraphQL variable declaration ($name: Type).
type Variable struct {
	Name string // variable name without the leading $
	Type string // GraphQL type string, e.g. "String!" or "CreateFileInput!"
}

// Operations holds all queries and mutations parsed from a GraphQL file.
type Operations struct {
	Queries   []Operation
	Mutations []Operation
}

// Find returns the first operation whose name matches (case-insensitive).
func (ops *Operations) Find(name string) (Operation, bool) {
	lower := strings.ToLower(name)
	for _, q := range ops.Queries {
		if strings.ToLower(q.Name) == lower {
			return q, true
		}
	}
	for _, m := range ops.Mutations {
		if strings.ToLower(m.Name) == lower {
			return m, true
		}
	}
	return Operation{}, false
}

// ExampleScenario is parsed from a .example.yaml fixture file.
// It enriches a single Bruno request with pre-filled variables and assertions.
type ExampleScenario struct {
	Name string `yaml:"name"`
	// Operation is the GraphQL operation name this example targets (e.g. "CreateInventoryItem").
	// Required: brunogen fails if this field is missing.
	Operation   string `yaml:"operation"`
	Description string `yaml:"description"`
	// Skip is a list of assertion blocks evaluated in the before-request script.
	// If any single assertion passes, bru.runner.skipRequest() is called.
	Skip   []TestAssertion `yaml:"skip"`
	Vars   map[string]any  `yaml:"vars"`
	Expect []TestAssertion `yaml:"expect"`
}

// TestScenario is parsed from a .test.yaml fixture file.
// It defines a multi-step flow that maps to one Bruno collection subdirectory.
type TestScenario struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Steps       []TestStep `yaml:"steps"`
}

// TestStep is one HTTP request within a TestScenario.
// Each step generates exactly one Bruno .yml file.
type TestStep struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Operation   string `yaml:"operation"` // required; GraphQL operation name
	Description string `yaml:"description"`
	// Skip is a list of assertion blocks evaluated in the before-request script.
	// If any single assertion passes, bru.runner.skipRequest() is called.
	Skip  []TestAssertion `yaml:"skip"`
	Vars  map[string]any  `yaml:"vars"`
	Tests []TestAssertion `yaml:"tests"`
}

// TestAssertion is one named group of assertions within a step.
// It maps to a single test() block in the generated Bruno script.
type TestAssertion struct {
	Msg        string      `yaml:"msg"`
	Assertions []Assertion `yaml:"assertions"`
}

// Assertion is a single expect() call within a TestAssertion block.
// Ref is the path to inspect; the assertion type is given by the camelCase key.
//
// Supported assertion keys:
//
//	equal, notEqual, nil, exists, empty, contains, isType,
//	greater, less, greaterOrEqual, lessOrEqual, len, regexp
//
// nil: true checks that the value does not exist (.to.not.exist).
// nil: false checks that the value exists (.to.exist).
// exists: true checks that the value exists (.to.exist).
// exists: false checks that the value does not exist (.to.not.exist).
// empty: true checks that the value is empty (.to.be.empty).
// empty: false checks that the value is not empty (.to.not.be.empty).
type Assertion struct {
	Ref  string // res.status | res.headers.<n> | res.body.<path> | req.body.variables.<path> | res[<id>].<path>
	Test string // assertion name, e.g. "equal"
	Args any    // expected value or argument
}

var assertionKeys = []string{
	"equal", "notEqual", "nil", "exists", "empty",
	"contains", "isType", "greater", "less", "greaterOrEqual", "lessOrEqual",
	"len", "regexp",
}

// UnmarshalYAML implements the go-yaml Unmarshaler interface.
func (a *Assertion) UnmarshalYAML(unmarshal func(any) error) error {
	var raw map[string]any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	if v, ok := raw["ref"].(string); ok {
		a.Ref = v
	}
	for _, key := range assertionKeys {
		if args, ok := raw[key]; ok {
			a.Test = key
			a.Args = args
			break
		}
	}
	return nil
}
