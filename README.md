[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fpyck-ai%2Fpyck.svg?type=shield&issueType=license)](https://app.fossa.com/projects/git%2Bgithub.com%2Fpyck-ai%2Fpyck?ref=badge_shield&issueType=license)

# pyck

pyck is the logistical backbone and API for future warehousing. Built as a modular microservices platform, it streamlines operations like data management, inventory tracking, and order processing, while supporting sophisticated workflows through robust foundations.

## Overview

- **Modular Architecture:**
  pyck is a collection of independent, self-contained services covering main data management, inventory tracking, order picking, receiving, file, and workflow processing.

- **Unified API:**
  A dedicated gateway aggregates GraphQL schemas from all services, providing a single, consistent endpoint for data interaction.

- **Docker-Enabled Environment:**
  Using Docker Compose and Task commands, setting up and running pyck is simple and efficient.

## Technology

pyck is built with **Go 1.25+** using a monorepo structure. It uses **GraphQL** with Apollo Federation for a unified API, **PostgreSQL** with Ent ORM for data management, **Temporal.io** for workflows, and **NATS.io** for messaging. Authentication is handled by **Zitadel** (OIDC/OAuth2), with observability via **OpenTelemetry** and **Jaeger**.

The platform enforces **multi-tenancy** at the database level and uses **UUID v7** for time-ordered entity IDs. All data models use **soft deletes** and are managed through **code-first schemas** that auto-generate database access code.

## Directory Structure

```
pyck/
├── backend/              # Backend microservices and shared code
│   ├── cli/              # Command-line interface utilities
│   ├── common/           # Shared libraries and utilities
│   ├── file/             # File service
│   ├── gateway/          # GraphQL gateway (federation)
│   ├── inventory/        # Inventory service
│   ├── main-data/        # Main data service
│   ├── management/       # Management service
│   ├── picking/          # Picking service
│   ├── receiving/        # Receiving service
│   ├── temporal/         # Temporal worker integration
│   ├── workflow/         # Workflow service
│   ├── workflowgen/      # Workflow code generation
│   └── workflowsdk/      # Workflow SDK
├── config/               # Configuration files
│   ├── compose/          # Docker Compose service definitions
│   ├── db/               # Database setup scripts
│   ├── nats/             # NATS messaging configuration
│   ├── setup/            # Local environment setup scripts
│   └── temporal/         # Temporal workflow configuration
├── docs/                 # Project documentation
├── scripts/              # Utility scripts
├── task/                 # Taskfile definitions
└── tests/                # Test files
```

### Service Structure

Each microservice follows a common structure with variations based on functionality:

```
service-name/
├── api/                  # GraphQL API schema definitions
├── cmd/                  # Service entry points (main.go)
├── core/                 # Business logic and configurations
├── ent/                  # Entity framework (data access layer)
│   ├── schema/           # Data models
│   ├── gen/              # Generated code (auto-generated, do not edit)
│   └── migrate/          # Database migrations
├── graph/                # GraphQL resolvers
├── model/                # GraphQL models
├── resolvers/            # GraphQL resolver implementations
└── services/             # Business service layer
```

Additional directories may include:
- `workflows/` - Temporal workflow definitions (in management service)
- `bootstrap/` - Service initialization code
- `utils/` - Utility functions

> [!TIP]
> When reviewing code, focus on handwritten files in `ent/schema/` and `ent/migrate/`. Files in `ent/gen/`, `model/`, and files marked with "Code generated" are auto-generated and should not be modified directly.

## Getting Started

### Prerequisites

- [Go v1.25+](https://golang.org/dl/)
- [Docker](https://docs.docker.com/get-docker/) or [podman](https://podman.io/docs/installation)
- [Docker Compose v2](https://docs.docker.com/compose/install/)
- [Task v3.45+](https://taskfile.dev) - *A task runner / build tool*
- [golangci-lint v2.4+](https://golangci-lint.run/usage/install/) - *Go linter aggregator for code quality*
- [Rover CLI v0.34+](https://www.apollographql.com/docs/rover/getting-started) - *Apollo GraphQL schema management tool*
- [gotestsum v1.13+](https://github.com/gotestyourself/gotestsum?tab=readme-ov-file#install) - *Enhanced test runner for Go*
- [mkcert](https://github.com/FiloSottile/mkcert) - *Simple tool for managing locally-trusted development certificates*

  > [!TIP]
  > If you are using *podman*, please refer to [this guide](https://podman-desktop.io/docs/compose/running-compose).
  >
  > If you are on *macOS*, please refer to [this issue](https://github.com/docker/for-mac/issues/2083).

### Setup Instructions

1. **Clone the Repository**
   ```sh
   git clone https://github.com/pyck-ai/pyck.git
   cd pyck
   ```

2. **Configure Local Development Environment**
   ```sh
   task local-setup
   ```
   
   > [!TIP]
   > Each service relies on specific environment variables. Please refer to the individual service documentation for configuration details.

3. **Build and Run the Services**
   ```sh
   task generate
   docker compose up --wait
   ```

## GraphQL API

The gateway service provides a unified GraphQL API with secure, tenant-isolated access to all microservices. Authentication uses Zitadel JWT tokens, and all list queries support Relay-style pagination. Use GraphQL Playground for interactive exploration during development.

## Development

pyck uses [Task](https://taskfile.dev) for all build operations. Common commands:

```bash
task generate      # Generate code from schemas
task test          # Run tests with coverage
task lint          # Run linters
task build         # Build binaries
```

Services can be run individually with `task run` (Docker) or `task debug` (with debugger). See [CONTRIBUTING.md](./CONTRIBUTING.md) for detailed development workflow and contribution guidelines.

### Integration Tests

Integration tests in `tests/` run against real services via the gateway.

```bash
task init up                           # Start all services
cd tests/cli
go test -v -count=1 ./tests/...        # Run CLI import/export tests
```

The auth token is read automatically from `config/keys/bootstrap.env` (generated by `task init`).

| Test suite | What it covers |
|---|---|
| `tests/cli` | Import/export round-trip for 17 entity types across 5 services |
| `tests/nats` | NATS JetStream connectivity and consumer management |

## Documentation

- **[docs.pyck.cloud](https://docs.pyck.cloud)** - Official documentation
- **[CONTRIBUTING.md](./CONTRIBUTING.md)** - Contribution workflow, code quality standards, testing requirements
- **[docs/COMMIT_STANDARDS.md](./docs/COMMIT_STANDARDS.md)** - Commit message format and branch naming
- **[AGENTS.md](./AGENTS.md)** - Guidelines for AI agents working on this codebase

## Community

Join the pyck community on Slack to ask questions, share feedback, and connect with other contributors and users.

[Join our Slack](https://join.slack.com/t/pyck-community/shared_invite/zt-3ulnckg7r-kBk6Spkeyk_DldYQpDmcxA)

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](./CONTRIBUTING.md) for commit standards, code quality requirements, and the pull request process.

## License

pyck is licensed under the [Functional Source License, Version 1.1, ALv2 Future License (FSL-1.1-ALv2)](./LICENSE.md). This means you can use, modify, and redistribute the software for any non-competing purpose. After two years, each version becomes available under the [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0).
