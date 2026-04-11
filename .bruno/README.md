# Bruno API Collections

This directory contains [Bruno](https://www.usebruno.com/) API collections for testing the pyck GraphQL APIs.

## Auto-Generated Content

**⚠️ These files are auto-generated via `brunogen`**

The Bruno collections in this directory are automatically generated based on the GraphQL schemas defined in each service's `api/graph` directory.

### Generation Process

- **Source**: GraphQL schema files in `backend/<service>/api/graph/*.graphql`
- **Tool**: `brunogen` (custom code generator)
- **Output**: Bruno collection files (`.bru`) organized by service

### Usage

1. Install [Bruno](https://www.usebruno.com/) desktop application
2. Open this directory as a collection in Bruno
3. Configure authentication and environment variables as needed
4. Test GraphQL queries and mutations against your local or deployed services

### Regeneration

To regenerate the Bruno collections after schema changes:

```bash
task generate  # Regenerates all code including Bruno collections
```

**Do not manually edit** the `.bru` files in this directory - changes will be overwritten on the next generation. Instead, copy the collection to keep your modifications local-only.
