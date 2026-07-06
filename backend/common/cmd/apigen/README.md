# apigen - GraphQL Client Query Generator

`apigen` is a code generator that automatically creates GraphQL client query files from server-side GraphQL schemas. It parses schema definitions (generated from Ent models) and generates client-side `.graphql` files with proper pagination support and field resolution.

## Purpose

Instead of manually maintaining client API query files, `apigen` automates the process by:

- Parsing server-side GraphQL schemas from `graph/*.graphql` files
- Detecting queries and mutations from the schema
- Generating client-side `.graphql` files with proper structure
- Adding pagination support for Connection types (Relay-style)
- Recursively expanding nested object types (up to 3 levels deep)
- Handling circular references and preventing infinite loops

## Hand-written operations

apigen auto-generates only root-field operations and intentionally skips
entity→entity edges (expanding unbounded relations bloated responses). That
leaves no auto-generated operation for **federated cross-service relations** —
e.g. `PickingOrder.customer`, which the picking subgraph roots but the main-data
subgraph resolves. Such a selection is only valid against the composed
supergraph, not any single subgraph schema.

For these, drop a hand-written operation file in `api/operations/*.graphql`
(configurable via `--operations`). apigen syntax-checks each file and
concatenates them into a generated `api/operations_gen.graphql`. That file lives
*outside* the `api/graph/*.graphql` glob on purpose: the gqlgenc/apigenc clients
(validated against the local subgraph schema, which lacks the cross-service
field) never see it, while `brunogen` — which runs operations through the
federated gateway — reads it alongside `apigen_gen.graphql`. A missing/empty
`api/operations/` directory is a no-op and removes any stale generated file.

## Usage

### As a go:generate Directive

Add to your service's `generate.go` file:

```go
//go:generate go tool apigen
```

Then run:

```bash
go generate
```

### As a Standalone Command

```bash
# From any service directory
go run github.com/pyck-ai/pyck/backend/common/cmd/apigen --schema=graph --output=api/graph

# With verbose output
go run github.com/pyck-ai/pyck/backend/common/cmd/apigen --schema=graph --output=api/graph --verbose

# Dry run (preview without writing files)
go run github.com/pyck-ai/pyck/backend/common/cmd/apigen --schema=graph --output=api/graph --dry-run
```

## Command-Line Flags

- `--schema` (required): Directory containing server-side GraphQL schema files (e.g., `graph`)
- `--output` (required): Directory where client `.graphql` files will be generated (e.g., `api/graph`)
- `--verbose`: Enable verbose logging
- `--dry-run`: Preview generation without writing files

## Generated Output

### For Queries

**Paginated queries** (returning `*Connection` types):

```graphql
query Files($after: Cursor, $first: Int, $before: Cursor, $last: Int, $orderBy: FileOrder, $where: FileWhereInput) {
  files(after: $after, first: $first, before: $before, last: $last, orderBy: $orderBy, where: $where) {
    totalCount
    pageInfo {
      hasNextPage
      hasPreviousPage
      startCursor
      endCursor
    }
    edges {
      cursor
      node {
        id
        name
        # ... all entity fields
      }
    }
  }
}
```

**Non-paginated queries**:

```graphql
query Node($id: ID!) {
  node(id: $id) {
    id
    # ... all fields
  }
}
```

### For Mutations

Mutations include all return type fields, with nested objects properly expanded:

```graphql
mutation CreateFile($input: CreateFileInput!) {
  createFile(input: $input) {
    file {
      id
      name
      contentType
      # ... all File fields
    }
    id
    preSignedUploadUrl
  }
}
```

## Features

### Automatic Field Resolution

- **Scalars**: Included directly (String, Int, Boolean, etc.)
- **Enums**: Included directly
- **Objects**: Recursively expanded up to 3 levels deep
- **Lists**: Element type is expanded
- **Interfaces/Unions**: Skipped (not currently supported for client queries)

### Nested Object Expansion

The generator recursively expands object fields:

```graphql
mutation CreateFile($input: CreateFileInput!) {
  createFile(input: $input) {
    file {           # Object type - expanded
      id
      name
      dataType {     # Nested object - expanded
        id
        slug
      }
    }
    id
    preSignedUploadUrl
  }
}
```

**Depth Limiting:**
- Maximum depth: 3 levels
- Prevents infinite loops with circular references
- Falls back to `id` field when depth limit reached

### Pagination Support

Queries returning `*Connection` types automatically get:
- Relay-style cursor pagination parameters (`after`, `before`, `first`, `last`)
- Order and filter parameters (`orderBy`, `where`)
- Complete Connection structure (`totalCount`, `pageInfo`, `edges`, `node`)

### File Naming

Generated files are named after the query/mutation:
- Query `files` → `files.graphql`
- Mutation `createFile` → `createFile.graphql`
- Query name is capitalized for the operation: `query Files(...)`
- Mutation name is capitalized for the operation: `mutation CreateFile(...)`

### Generated File Headers

All generated files include:

```graphql
# Code generated by apigen. DO NOT EDIT.
# Generated at: 2025-12-23T15:15:58Z
```

## Integration with Workflow

### Generation Pipeline

The typical code generation workflow:

1. **Ent Schema** (`ent/schema/*.go`) → defines data models
2. **entgen** → generates `ent/gen/*` code
3. **gqlgen** → generates `graph/ent.graphql` server schema
4. **apigen** ← generates `api/graph/*.graphql` client queries
5. **gqlgenc** → generates `api/client_gen.go` client code

### Example Integration

In `backend/file/generate.go`:

```go
package file

//go:generate go tool entgo.io/ent/cmd/ent generate --feature sql/upsert --feature intercept --feature sql/execquery --feature entql --feature schema/snapshot ./ent/schema
//go:generate go tool gqlgen --config gqlgen.yml
//go:generate go tool apigen --schema=graph --output=api/graph
//go:generate go tool gqlgenc --configdir .
```

Running `go generate` executes all generators in sequence.

## Skipped Types

The generator automatically skips:

- **Interface types** (e.g., `Node`)
- **Union types**
- **Introspection fields** (`__typename`, `__schema`, etc.)

These types either don't have concrete implementations or are handled by the GraphQL runtime.

## Example: Complete File Service Generation

**Input Schema** (`graph/ent.graphql`):

```graphql
type File implements Node {
  id: ID!
  name: String!
  contentType: String!
  # ... more fields
}

type FileConnection {
  edges: [FileEdge]
  pageInfo: PageInfo!
  totalCount: Int!
}

type Query {
  files(after: Cursor, first: Int, orderBy: FileOrder, where: FileWhereInput): FileConnection!
}

type Mutation {
  createFile(input: CreateFileInput!): CreateFileResult
}

type CreateFileResult {
  id: ID!
  file: File
  preSignedUploadUrl: String
}
```

**Generated Output**:

`api/graph/files.graphql`:
```graphql
query Files($after: Cursor, $first: Int, $before: Cursor, $last: Int, $orderBy: FileOrder, $where: FileWhereInput) {
  files(after: $after, first: $first, before: $before, last: $last, orderBy: $orderBy, where: $where) {
    totalCount
    pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
    edges {
      cursor
      node { id name contentType # ... }
    }
  }
}
```

`api/graph/createFile.graphql`:
```graphql
mutation CreateFile($input: CreateFileInput!) {
  createFile(input: $input) {
    file { id name contentType # ... }
    id
    preSignedUploadUrl
  }
}
```

## Architecture

### Core Components

1. **`main.go`**: CLI interface and orchestration
2. **`schema.go`**: Schema loading and parsing (uses `vektah/gqlparser/v2`)
3. **`generator.go`**: Query/mutation generation logic with recursive field expansion

### Type Handling Logic

```go
// Simplified flow for field generation
func generateTypeFields(typeName string) string {
    typeDefinition := schema.Types[typeName]
    
    for each field in typeDefinition.Fields {
        if field is Scalar or Enum:
            add field name
        else if field is Object:
            recursively expand object (depth-limited)
        else if field is List:
            expand element type
        else:
            skip (Interface/Union)
    }
}
```

### Circular Reference Prevention

- Tracks visited types per expansion path
- Limits recursion depth to 3 levels
- Falls back to `id` field when limits reached

## Troubleshooting

### "File is duplicated" Error

**Cause**: Query name conflicts with entity type name (e.g., `file: ServiceInfo!` vs `File` entity).

**Solution**: Remove or rename conflicting queries in server schema.

### Missing Fields in Generated Queries

**Cause**: Fields might be defined in type extensions in separate files.

**Solution**: Ensure `schema.go` loads all `.graphql` files from the schema directory.

### Object Fields Not Expanded

**Cause**: Generator depth limit reached or circular reference detected.

**Solution**: Check object nesting depth. The generator limits expansion to 3 levels and includes `id` field as fallback.

## Related Tools

- **apigenc**: Different tool for API client generation (DO NOT confuse with `apigen`)
- **gqlgen**: Server-side GraphQL code generator
- **gqlgenc**: Client-side GraphQL code generator (uses output from `apigen`)
- **entgen**: Ent ORM code generator

## Development

To modify the generator:

1. Edit files in `backend/common/cmd/apigen/`
2. Test with: `cd backend/file && go generate`
3. Verify generated output in `backend/file/api/graph/`
4. Check integration with gqlgenc

## Version History

- **v1.0.0** (2025-12-23): Initial release
  - Automatic query/mutation detection
  - Pagination support for Connection types
  - Recursive object field expansion (depth: 3)
  - Circular reference prevention
  - Interface/Union type skipping