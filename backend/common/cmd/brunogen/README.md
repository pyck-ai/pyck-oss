# brunogen

Generates Bruno API client YAML files from GraphQL schema files and YAML test fixtures.

## Documentation

Full documentation — file formats, `$fake`/`$ref` syntax, cross-step variable flow,
assertion syntax, output structure, and all CLI flags:

```bash
go doc github.com/pyck-ai/pyck/backend/common/cmd/brunogen
```

## Quick start

```bash
# Build
go build -o brunogen .

# Run from a service directory (e.g. backend/file)
brunogen

# Dry-run preview
brunogen --verbose --dry-run
```
