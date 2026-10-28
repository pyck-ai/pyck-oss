[![Release](https://img.shields.io/github/v/tag/pyck-ai/pyck-oss?sort=semver&label=release)](https://github.com/pyck-ai/pyck-oss/tags)
[![Docs](https://img.shields.io/website?url=https%3A%2F%2Fdocs.pyck.cloud&label=docs&up_message=docs.pyck.cloud&up_color=blue&down_message=down)](https://docs.pyck.cloud)
[![Slack](https://img.shields.io/badge/slack-pyck--community-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/pyck-community/shared_invite/zt-3ulnckg7r-kBk6Spkeyk_DldYQpDmcxA)

# pyck

<!-- toc
## Contents

- [What is pyck?](#what-is-pyck)
- [Getting started](#getting-started)
- [Documentation](#documentation)
- [Contributing](#contributing)
- [License](#license)
- [Security](#security)
-->

pyck is the logistical backbone and API for future warehousing.

## What is pyck?

pyck covers the data and processes of a warehouse (master data, inventory, receiving, picking, files, workflows) as independent services behind one federated GraphQL gateway, so clients talk to a single API.

It is a Go monorepo. The API uses GraphQL with Apollo Federation, data lives in PostgreSQL through the Ent ORM, workflows run on Temporal, services exchange messages over NATS, and Zitadel (OIDC/OAuth2) handles authentication. Observability uses OpenTelemetry and Jaeger. Every entity has a UUID v7 ID, rows are soft-deleted, and schemas are code-first: the Ent schema generates the database access code.

Out of the box, pyck includes:

- **A federated GraphQL API:** the gateway composes every service's schema into one supergraph; see [backend/gateway/README.md](backend/gateway/README.md).
- **Multi-tenancy:** the database layer scopes every query and mutation to the request's tenants; see [docs/tenant-access-control.md](docs/tenant-access-control.md).
- **Tenant registration:** the management service provisions tenants and their identity; see [docs/tenant-registration.md](docs/tenant-registration.md).
- **Workflows:** Temporal workflows register through a Go SDK; see [backend/workflowsdk/README.md](backend/workflowsdk/README.md).
- **Identity and access:** Zitadel provides login and tokens; see [docs/zitadel-setup.md](docs/zitadel-setup.md).
- **Graceful shutdown:** a defined shutdown ladder for the GraphQL services; see [docs/graceful-shutdown.md](docs/graceful-shutdown.md).

## Getting started

```sh
git clone https://github.com/pyck-ai/pyck.git && cd pyck   # clone the repository
task local-setup   # first run only: bootstrap local credentials in config/keys/
task up            # build and start the stack with docker compose; safe to repeat
task test          # run the unit tests of every module
```

`task local-setup` deletes the credentials in `config/keys/` and needs a fresh Zitadel database, so run it once per stack lifetime and never again after the stack exists.

### Prerequisites

- [Go](https://go.dev/dl/) 1.27+
- [Docker](https://docs.docker.com/get-docker/) or [podman](https://podman.io/docs/installation), with Compose v2 (podman: see the [Podman Desktop Compose guide](https://podman-desktop.io/docs/compose/running-compose); macOS: see [docker/for-mac#2083](https://github.com/docker/for-mac/issues/2083))
- [Task](https://taskfile.dev) 3.47+
- [golangci-lint](https://golangci-lint.run/usage/install/) 2.4+
- [Rover](https://www.apollographql.com/docs/rover/getting-started) 0.34+
- [gotestsum](https://github.com/gotestyourself/gotestsum#install) 1.13+
- [mkcert](https://github.com/FiloSottile/mkcert)

Set `PYCK_NATS_AUTH_KEY_SEED` in `.env.local` (gitignored, never commit it); [`.env.example`](.env.example) explains it.

### More

Run `task --list` for every other command. For integration tests against a running stack, read [tests/integration/AGENTS.md](tests/integration/AGENTS.md).

## Documentation

- [docs.pyck.cloud](https://docs.pyck.cloud): the official documentation.
- [CONTRIBUTING.md](CONTRIBUTING.md): org rules, local checks and where to ask.
- [docs/COMMIT_STANDARDS.md](docs/COMMIT_STANDARDS.md): the CI commit-message check and where it is stricter than the org guideline.
- [docs/tenant-access-control.md](docs/tenant-access-control.md): `X-Pyck-Tenant-Id` semantics, role and liveness authorization, access for disabled tenants.
- [docs/tenant-registration.md](docs/tenant-registration.md): how `registerTenant` provisions a tenant.
- [docs/graceful-shutdown.md](docs/graceful-shutdown.md): shutdown ladder, time budget and sizing the stop grace period.
- [docs/zitadel-setup.md](docs/zitadel-setup.md): the Zitadel v4 components and their setup.
- [docs/adr/README.md](docs/adr/README.md): architecture decision records.
- [backend/gateway/README.md](backend/gateway/README.md): the federation gateway, supergraph generation and adding a subgraph.
- [backend/workflowsdk/README.md](backend/workflowsdk/README.md): the Temporal workflow SDK.
- [tests/integration/AGENTS.md](tests/integration/AGENTS.md): end-to-end suites against a live stack, cadences and rules for new suites.
- [hack/stockfix/README.md](hack/stockfix/README.md): operator tool that repairs the inventory stock ledger.

## Contributing

Start with [CONTRIBUTING.md](CONTRIBUTING.md). Read [AGENTS.md](AGENTS.md) first if you are an AI coding agent.

## License

pyck is licensed under the [Functional Source License, Version 1.1, ALv2 Future License (FSL-1.1-ALv2)](LICENSE.md). You can use, modify, and redistribute the software for any non-competing purpose. After two years, each version becomes available under the [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0).

## Security

Report vulnerabilities privately; see the org [security policy](https://github.com/pyck-ai/.github/blob/main/.github/SECURITY.md).
