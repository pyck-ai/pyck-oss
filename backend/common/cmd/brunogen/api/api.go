/*
Package api is the public API for brunogen.

brunogen generates Bruno API client YAML files from GraphQL schema files and
optional YAML test fixtures.  Two output collections are produced:

  - examples/ – one file per GraphQL operation, optionally enriched with data
    from testdata/examples/<opname>.example.yaml
  - tests/     – one subdirectory per scenario, one file per step, driven by
    testdata/tests/<name>.test.yaml

# Dependency graph

	main → pkg/api → { pkg/types, internal }
	                   internal → pkg/types

# Testdata file formats

Example file (testdata/examples/<opname>.example.yaml):

	name:        string   # required
	description: string
	vars:
	  input:
	    field: {$fake: uuid}
	expect:
	  - msg: "Status is 200"
	    assertions:
	      - ref: res.status
	        equal: 200

Test scenario file (testdata/tests/<name>.test.yaml):

	name:        string   # required
	description: string
	steps:
	  - id:        string   # required when referenced by later steps
	    name:      string
	    operation: string   # required; GraphQL operation name
	    vars:      map
	    tests:
	      - msg: "..."
	        assertions: [...]

# $fake and $ref substitution

$fake resolves {{typeName}} placeholders to platform-specific random values:

	field: {$fake: "{{uuid}}"}               →  {{$randomUUID}}
	field: {$fake: "{{productname}}-{{nonce}}"}  →  {{$randomProductName}}-{{$timestamp}}-1
	field: {$fake: "{{int}}"}                →  {{$randomInt}}   (unquoted)
	field: {$fake: uuid}                     →  {{$randomUUID}}  (scalar shorthand)

$ref references another value in the current or a previous step:

	{$ref: res.status}
	{$ref: res.headers.<name>}
	{$ref: res.body.<path>}
	{$ref: req.body.variables.<path>}
	{$ref: "res[<stepID>].<path>"}       ← cross-step

Cross-step refs generate bru.setVar in the source step and bru.getVar
at every consumption site, using a stable variable name:

	bru-test_<scenarioBasename>_<8hexchars>

Array wildcard ([] before last path segment) expands to a forEach loop:

	res.body.data.files.edges[].node.id
*/
package api

import (
	"fmt"

	gen "github.com/pyck-ai/pyck/backend/common/cmd/brunogen/internal"
	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

// prepare fills in auto-detected config fields and parses the GraphQL file.
// Used by the examples subcommand.
func prepare(cfg types.Config) (*types.Operations, types.Config, error) {
	if cfg.ServiceName == "" {
		name, err := DetectServiceName(cfg.GraphQLFile)
		if err != nil {
			return nil, cfg, fmt.Errorf("failed to detect service name: %w", err)
		}
		cfg.ServiceName = name
		gen.LogVerbosef(cfg.Verbose, "Auto-detected service name: %s", cfg.ServiceName)
	}
	if cfg.TestdataDir == "" {
		cfg.TestdataDir = TestDataDir(cfg.GraphQLFile)
	}
	ops, err := ParseGraphQL(cfg.GraphQLFile)
	if err != nil {
		return nil, cfg, fmt.Errorf("failed to parse GraphQL file: %w", err)
	}
	gen.LogVerbosef(cfg.Verbose, "Found %d queries and %d mutations", len(ops.Queries), len(ops.Mutations))
	return ops, cfg, nil
}
