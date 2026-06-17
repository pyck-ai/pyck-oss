# pyck

pyck is a modular microservices platform for warehouse operations.

## For AI Agents

This file contains essential guidelines for AI agents working on this codebase.

### Documentation

- [README.md](./README.md) - Project overview and directory structure
- [CONTRIBUTING.md](./CONTRIBUTING.md) - Contribution workflow and code quality standards
- [docs/COMMIT_STANDARDS.md](./docs/COMMIT_STANDARDS.md) - Commit message and branch naming standards

### Architecture Overview

**Technology Stack:**
- **Backend**: Go 1.25+ with go.work monorepo structure
- **API**: GraphQL with Apollo Federation (gateway pattern)
- **Database**: PostgreSQL with Ent ORM for code-first schema management
- **Workflow Engine**: Temporal.io for durable workflows
- **Messaging**: NATS.io with JetStream for event streaming
- **Authentication**: Zitadel OIDC/OAuth2
- **Observability**: OpenTelemetry (OTLP), Jaeger tracing
- **Storage**: MinIO (S3-compatible)
- **Container Orchestration**: Docker Compose for local dev, Kubernetes for production

**Microservices:**
- `main-data` - Core data entities (customers, suppliers)
- `inventory` - Stock levels, item movements, transactions
- `management` - Company configurations, workflow definitions
- `picking` - Order picking and processing
- `receiving` - Inbound order processing
- `file` - Document and file storage
- `workflow` - Background task execution and workflow state
- `gateway` - Apollo Router for GraphQL federation
- `temporal` - Custom Temporal server with authorization

**Shared Infrastructure:**
- `common` - Shared Go libraries (auth, logging, database, HTTP utilities)
- `workflowsdk` - SDK for building Temporal workflows
- `workflowgen` - Code generator for workflow registration

### Code Review

**Review these files:**
- `ent/schema/*` - Data models (handwritten)
- `ent/migrate/migration/*` - Database migrations (handwritten)
- All Go source files
- Configuration files (`*.yaml`, `*.yml`, `go.mod`, `go.work`)
- Documentation (`.md` files)

**Ignore these files:**
- `ent/gen/*` - Auto-generated code from Ent
- `model/*_gen.go` - Auto-generated GraphQL models
- Files with `// Code generated` header
- Vendor directories

**Data Model Patterns:**
- All entities use common mixins: `TenantMixin`, `DataMixin`, `HistoryMixin`, `LimitMixin`
- UUIDs are v7 (time-ordered) via `uuidgql.GenerateV7UUID`
- Soft deletes via `HistoryMixin` (deleted_at timestamp)
- Multi-tenancy enforced at database level via `tenant_id`
- Custom JSON data fields for flexibility (`*_json_data` fields)

### Development Workflow

**Setup:**
```bash
task local-setup    # One-time setup (certificates, configs)
task generate       # Generate code from schemas
docker compose up --wait
```

**Development:**
```bash
# Run specific service
cd backend/<service>
task run           # Run in Docker
task debug         # Run with Delve debugger
task logs          # View logs

# Code generation
task generate      # Generate Ent, GraphQL code
go generate        # Run go:generate directives

# Testing
task test          # Run tests with coverage
task lint          # Run golangci-lint
task build         # Build binaries
```

**Key Tools:**
- [Task](https://taskfile.dev) - Task runner (replace Make)
- [gotestsum](https://github.com/gotestyourself/gotestsum) - Enhanced test output
- [golangci-lint](https://golangci-lint.run) - Aggregated linter
- [Rover CLI](https://www.apollographql.com/docs/rover/) - GraphQL schema management
- [mkcert](https://github.com/FiloSottile/mkcert) - Local TLS certificates
- [Renovate](https://docs.renovatebot.com) - Automated dependency updates

### Testing

Before committing:
1. `task test` - All tests must pass
2. `task lint` - No linting errors
3. `task build` - Build must succeed

**Test Organization:**
- Unit tests: `*_test.go` files alongside source
- Integration tests: Require database/external services
- Test fixtures: Use `gofakeit` for realistic data generation
- Coverage reports: Generated during CI test runs

### Deployment

**Environments:**
- **Local**: Docker Compose with debug capabilities
- **Dev**: Auto-deployed on main branch merge
- **Feature**: Auto-deployed per feature branch
- **Production**: Manual trigger via GitHub Actions

**CI/CD Pipeline:**
1. **Test** (`test.yml`): Lint, test, coverage analysis per service
2. **Build** (`container-build.yml`): Build multi-arch Docker images
3. **Deploy** (`deploy.yml`): Trigger deployment in separate repo
4. **Version Check** (`go-version-check.yml`): Ensure Go version consistency

**Container Build:**
- Multi-stage builds (builder + runtime)
- Base image: `ghcr.io/pyck-ai/baseimages/alpine`
- Build caching via Docker BuildKit
- Automatic tagging with Git commit SHA
- Memory limits enforced for all services

### Code Quality Standards

**Go Code:**
- Follow Go idioms and effective Go patterns
- Handle errors explicitly (no panic in production code)
- Add tests for new features (aim for >80% coverage)
- Document all exported functions, types, and packages
- Use contexts for cancellation and timeouts
- Prefer composition over inheritance
- Use dependency injection for testability

**GraphQL:**
- Use directives for Federation (`@key`, `@external`, `@requires`)
- Implement Relay-style pagination for lists
- Add ordering fields via `entgql.OrderField`
- Use mutations for write operations only

**Temporal Workflows:**
- Workflows MUST be deterministic (no randomness, no wall-clock time)
- Workflow structs MUST NOT contain state (instantiated once per worker)
- Activity structs CAN contain shared state (DB connections, caches)
- Use `workflowgen` for automatic workflow registration
- Always set activity timeouts

**GitHub Actions JavaScript:**
- Avoid deeply nested if-else chains
- Extract complex logic into functions
- Use descriptive variable names
- Prefer early returns over nested conditions

### Security Guidelines

- **Never commit secrets, tokens, or credentials**
- **Always validate user input** at GraphQL resolver level
- **Follow principle of least privilege**
- **Use Zitadel for authentication** (JWT tokens)
- **Enforce tenant isolation** in all queries via `TenantID` filter
- **SQL injection prevented** by Ent parameterized queries
- **Report security concerns** to maintainers immediately

### Common Pitfalls

**Database Operations:**
- Forgetting `TenantID` filter in queries → data leakage across tenants
- Missing `NotDeletedFilter()` → returning soft-deleted records
- Not handling `NotFound` errors → panics on missing data
- Editing generated `ent/gen/*` files → changes lost on regeneration

**Temporal Workflows:**
- Using `time.Now()` in workflows → non-deterministic, breaks replay
- Storing state in workflow structs → lost on workflow restarts
- Missing activity timeouts → workflows hang indefinitely
- Forgetting to register workflows → runtime errors

**Code Generation:**
- Editing `*_gen.go` files → changes overwritten by `task generate`
- Not running `task generate` after schema changes → build failures
- Modifying generated GraphQL models → sync issues with schema

### Performance Considerations

- Services are memory-optimized for containerized environments
- PostgreSQL tuned for low-memory operation (see `db.yaml`)
- Go runtime tuned with `GOGC` and `GOMEMLIMIT`
- Connection pooling configured per service
- GraphQL N+1 queries prevented by Ent dataloaders
- Temporal workers limited by `MaxConcurrentActivityExecutionSize`

### Getting Help

- Check service README files for specific configurations
- Review existing tests for usage examples
- Consult `backend/workflowsdk/README.md` for workflow patterns
- Use `task --list` to see available commands
- Ask in team channels for architecture questions