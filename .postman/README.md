# Postman API Collections

This directory contains [Postman](https://www.postman.com/) API collections for testing the pyck GraphQL APIs.

## Manual Generation from Bruno

**⚠️ The Postman collection is manually generated from Bruno collections**

The `pyck.json` file in this directory is created by manually converting the auto-generated Bruno collections to Postman format.

### Generation Workflow

1. **Bruno collections** are auto-generated via `brunogen` from GraphQL schemas (see `../.bruno/README.md`)
2. **Postman collection** is manually exported from Bruno:
   - Open the `.bruno` directory in Bruno desktop application
   - Use Bruno's export functionality to convert to Postman format
   - Save the exported collection as `.postman/pyck.json`

### Usage

Import `pyck.json` into Postman to access the complete API collection for all pyck services.
