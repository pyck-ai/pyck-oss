/*
brunogen generates Bruno API client YAML files from GraphQL schema files and
optional YAML test fixtures.

# Usage

brunogen has two subcommands.

## examples – per-service, one file per GraphQL operation

Run from a service directory (e.g. backend/inventory):

	brunogen examples [flags]

	-g, --graphql   Path to apigen_gen.graphql  (default: ./api/graph/apigen_gen.graphql)
	-s, --service   Service name                 (auto-detected from --graphql path if omitted)
	-o, --output    Examples output directory    (default: ../../.bruno/examples)
	-v, --verbose   Log generated file paths
	    --dry-run   Preview without writing files

## tests – cross-service, multi-step scenarios

Can be run from any directory; --backend-dir points to the parent of all service
directories and brunogen reads each service's apigen_gen.graphql directly:

	brunogen tests [flags]

	-s, --service       Service name             (auto-detected from working directory if omitted)
	    --testdata       Path to testdata dir     (default: ./api/testdata)
	    --backend-dir    Path to backend/ dir     (default: ../)
	    --output-tests   Tests output directory   (default: ../../.bruno/tests)
	-v, --verbose        Log generated file paths
	    --dry-run        Preview without writing files

# How it works

brunogen reads an apigen_gen.graphql file produced by apigen and writes one
YAML file per GraphQL operation into a Bruno collection directory.  When
test-data fixtures are present the generated files also include pre-filled
request variables and Chai test assertions.

Two output collections are produced:

  - examples/ – one file per operation, optionally enriched with data from
    testdata/examples/<opname>.example.yaml
  - tests/     – one subdirectory per scenario, one file per step, driven by
    testdata/tests/<name>.test.yaml

# Package layout

	brunogen/
	  main.go       CLI entry point (this package)
	  doc.go        this file
	  api/          public API: GenerateExamples, GenerateScenarios,
	                DetectServiceNameFromDir, …
	  types/        domain models: Config, Operation, Operations, TestScenario,
	                TestStep, ExampleScenario, Assertion, sentinel errors
	  internal/     rendering helpers, template execution, $fake/$ref logic
	                (not importable outside this module)

# Output structure

	.bruno/
	  examples/
	    <service>/            ← folder.yml
	      <resource>/         ← folder.yml
	        <opname>_gen.yml
	        ...
	  tests/
	    <service>/            ← folder.yml
	      <scenario-slug>/    ← folder.yml
	        01_<opname>_gen.yml
	        02_<opname>_gen.yml
	        ...

Scenario directories are named by slugifying the scenario's "name" field
(e.g. "Item movement lifecycle" → "item-movement-lifecycle").  Step files carry
a two-digit prefix so Bruno executes them in declaration order.

# Testdata file formats

## Example files (testdata/examples/<opname>.example.yaml)

Enriches a single Bruno request with pre-filled variables and assertions.
The file name must match the GraphQL operation name (case-insensitive).

	name:        string   # required; human-readable title
	description: string   # explains what the operation does
	vars:
	  <variableName>: {$fake: uuid}
	  input:
	    field: {$fake: productname}
	skip:                 # optional; skip the request when any assertion passes
	  - msg: "..."
	    assertions: [...]
	expect:               # renamed from "tests" in example files
	  - msg: "Status is 200"
	    assertions:
	      - ref: res.status
	        equal: 200

## Test scenario files (testdata/tests/<name>.test.yaml)

Defines a multi-step flow; one subdirectory with one Bruno file per step.

	name:        string   # required; used as the directory slug
	description: string   # describes the end-to-end scenario
	steps:
	  - id:          string   # optional; required when this step is referenced by later steps
	    name:        string   # human-readable step title
	    operation:   string   # required; must be qualified: "service.OperationName"
	    description: string   # written to /docs in the generated .yml file
	    skip:        [...]    # optional; same as example skip (see above)
	    vars:        map      # input variables; supports $fake and $ref
	    tests:                # test assertions for this step
	      - msg: "..."
	        assertions: [...]

Operation names in test scenarios MUST be fully qualified as "service.OperationName"
(e.g. "inventory.createInventoryItem") because tests are cross-service and brunogen
looks up the GraphQL operation from each service's apigen_gen.graphql file.

# Variable substitution

## $fake – random data

	field: {$fake: "{{<type>}}"}

Resolves each {{typeName}} placeholder to a platform-specific random value.
For Bruno, this becomes a dynamic variable, e.g. {{uuid}} → {{$randomUUID}}.
Numeric types (bool, int, integer) are emitted unquoted in JSON.

Multiple types can be composed with literal text between the placeholders:

	name: {$fake: "{{productname}}-{{nonce}}"}

This produces a string like "Awesome Widget-{{$timestamp}}-1" which is unique
across parallel test runs and separate brunogen invocations.

As a shorthand, a plain scalar (no {{}} delimiters) is treated as a single type:

	field: {$fake: uuid}    →  same as {$fake: "{{uuid}}"}

Special types (not in the standard variables list):

	nonce   "{{$timestamp}}-{seq}" — unique both across runs (Bruno $timestamp)
	        and within a run (scenario-scoped counter).

Supported $fake types (all case-insensitive):

	  Identifiers & Basic:
		uuid, guid, nanoid, alphanumeric, bool / boolean, int / integer,
		timestamp, isotimestamp

	  Names & Personal:
		firstname, lastname, fullname / name, nameprefix, namesuffix,
		username, email, exampleemail, phone, phoneext,
		jobarea, jobdescriptor, jobtitle, jobtype

	  Internet & Network:
		domain, domainsuffix, domainword, ip, ipv4, ipv6, mac,
		password, protocol, semver, url, useragent, locale

	  Location:
		city, country, countrycode, lat, lon, street, streetname

	  Commerce & Business:
		sku (composite: productname + bankaccount),
		product, productname, productadjective, productmaterial, department, price,
		company, companysuffix, bs, bsadjective, bsbuzz, bsnoun,
		catchphrase, catchphraseadjective, catchphrasedescriptor, catchphrasenoun

	  Finance:
		bankaccount, bankaccountname, bic, bitcoin, creditcard,
		currency, currencyname, currencysymbol, iban, transactiontype

	  Text & Lorem:
		word, words, adjective, noun, verb, ingverb, phrase,
		paragraph / sentence,
		loremlines, loremparas, loremsentences, loremslug, loremtext, loremword, loremwords

	  Dates:
		datefuture, datepast, daterecent, month, weekday

	  Files & System:
		filename, commonfilename, commonfileext, commonfiletype,
		fileext, filetype, filepath, dirpath, mimetype

	  Images:
		avatar, image, abstractimage, animalsimage, businessimage, catsimage,
		cityimage, foodimage, nightlifeimage, fashionimage, peopleimage,
		natureimage, sportsimage, transportimage, imagedatauri

	  Colors:
		color, hexcolor, abbreviation

	  Database:
		dbcollation, dbcolumn, dbengine, dbtype

## $placeholder – manual placeholder

	field: {$placeholder: "ACTION REQUIRED: description"}

Emits the string verbatim and inserts a prominent ACTION REQUIRED comment block
in the generated Bruno file. Use for IDs or values that cannot be pre-generated
and must be filled in manually before running the request.

## $ref – reference another value

	field: {$ref: <path>}

Ref path grammar:

	res.status                             HTTP response status code
	res.headers.<name>                     HTTP response header
	res.body.<dotted-path>                 JSON response body field
	req.body.variables.<dotted-path>       GraphQL request variable
	res[<stepID>].<path>                   Value from a previous step (cross-step)

Cross-step refs require the source step to have an "id:" field. brunogen emits
a bru.setVar call in the source step and bru.getVar at every consumption site,
keyed by a stable collection variable name:

	bru-test_<scenarioBasename>_<8hexchars>

where the hex suffix is the FNV-32a hash of the fully-qualified ref string.

Array wildcard — [] before the last path segment — expands to a forEach loop:

	res.body.data.files.edges[].node.id

	→  body.data.files.edges.forEach(function(item) {
	       expect(item.node.id)<assertion>;
	   });

# Assertion syntax

Each entry in an assertions list has a "ref" key and exactly one assertion key:

	ref:            res.* or req.* path to inspect
	equal:          value    → .to.equal(value)
	notEqual:       value    → .to.not.equal(value)
	nil:            true     → .to.not.exist       (value is null/undefined)
	nil:            false    → .to.exist            (value is present; replaces notNil)
	exists:         true     → .to.exist
	exists:         false    → .to.not.exist
	empty:          true     → .to.be.empty
	empty:          false    → .to.not.be.empty     (replaces notEmpty)
	contains:       value    → .to.include(value)
	isType:         string   → .to.be.a(type)
	greater:        number   → .to.be.greaterThan(n)
	less:           number   → .to.be.lessThan(n)
	greaterOrEqual: number   → .to.be.gte(n)
	lessOrEqual:    number   → .to.be.lte(n)
	len:            number   → .to.have.lengthOf(n)
	regexp:         string   → .to.match(new RegExp(pattern))

Assertion values may themselves be $ref maps to compare against captured or
request values:

  - ref: res.body.data.replenishmentOrderItems.edges[].node.sku
    equal:
    $ref: res[createOrderItem].body.data.createReplenishmentOrderItem.replenishmentOrderItem.sku

# Writing standards

## Assertion ordering

Every step that performs a real request must assert, in order:

 1. res.status equal 200
 2. res.body.errors nil: true  (no GraphQL errors)
 3. Domain-specific assertions (IDs not nil, field values correct, counts, etc.)

## Cleanup: always clean up created entities

Every entity created during a scenario must be deleted at the end.
If a deletion is expected to fail (e.g. deleting an executed movement), replace
the cleanup step with a negative test that asserts the failure, and add a
separate cleanup step using an alternative mechanism (e.g. deleting the item or
clearing stock first).

## Relay pagination conventions

List operations return Relay-style pagination objects.  Use these paths:

	res.body.data.<queryName>.totalCount          total number of results
	res.body.data.<queryName>.edges[].node.<field> field on each result
	res.body.data.<queryName>.pageInfo.hasNextPage pagination cursor

## Negative tests

Business rule violations must be explicitly tested. Common patterns:

  - Deleting an executed movement: assert res.body.errors nil: false
  - Deleting a collection that contains active movements: assert res.body.errors nil: false
  - Creating a movement that exceeds available stock: assert res.body.errors nil: false

Negative tests must still assert res.status equal: 200 (GraphQL always returns HTTP 200;
errors are in the body).

## Coverage requirements

Every GraphQL operation (query and mutation) must have:
  - An example file in testdata/examples/<opname>.example.yaml
  - At least one test scenario step that exercises the operation in testdata/tests/

Lifecycle scenarios (create → read → update → delete) cover the common CRUD path.
Separate test files should cover:
  - Deletion of unexecuted vs executed entities when the rules differ
  - Negative cases for business constraints
  - Cross-service flows where operations from multiple services interact
*/
package main
