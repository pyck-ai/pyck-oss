# ADR-0001: Use a Dedicated Init Service for Cluster Bootstrap

## Status

Accepted

## Context

Bringing up a fresh pyck cluster requires seeding three external systems before
any application service can authenticate or store data:

- **Zitadel**: organizations, projects, OIDC apps, machine users, human users,
  roles, and PAT/JWT credentials for every service
- **Temporal**: a namespace and search attribute registration
- **MinIO**: the S3 bucket used by the file service

### The previous approach

Bootstrap was performed by a set of Temporal workflows running inside the
management service:

- `zitadel-setup`: a multi-activity Temporal workflow (~1 000 lines) that called
  the Zitadel admin gRPC API to create all entities
- `temporal-setup`: a smaller workflow that registered the Temporal namespace
- A CLI command (`instructions/setup.go`) with imperative setup logic for local
  development
- Two shell scripts (`local-zitadel-setup.sh`, `local-minio-setup.sh`) for
  local Compose-based setup

This approach had several compounding problems.

**Self-referential dependency.** Temporal workflows require the workflow engine
itself to be healthy before they can run. But the workflow engine's namespace
must be registered as part of bootstrap, which meant Temporal was being asked
to bootstrap itself. In practice this was worked around with ordering hacks and
retry loops, but the underlying circularity made the sequence fragile.

**Determinism constraints.** Temporal workflows must be fully deterministic
so that they can be replayed from event history. Bootstrap operations are
inherently non-deterministic: they call external REST and gRPC APIs, sleep
waiting for services to become healthy, and depend on external state. Every
Zitadel API call had to be wrapped in an activity, turning a sequential script
into a scattered workflow/activity split with significant boilerplate.

**Monolithic entanglement.** Bootstrap logic lived inside the management service.
The management service could not start until bootstrap completed, but bootstrap
ran as a set of workflows launched by management on startup, creating a circular
ordering that required management to partially initialize before bootstrap could run.

**Slow local iteration.** Changing a single Zitadel seed step required modifying
a workflow and/or its activities, recompiling the management binary, and waiting
for a full Docker Compose restart cycle. Re-running bootstrap was not safe
without wiping Zitadel state because there were no idempotency guards; any
duplicate resource would cause a workflow failure.

**Credential export was manual.** After the workflows ran, credentials (PATs,
OIDC client secrets, app keys) had to be manually copied into env files or
Kubernetes Secrets. There was no automated export path for OIDC client secrets,
which are only returned once by the Zitadel API.

### Forces

- Bootstrap must complete before management, inventory, picking, receiving, file,
  and workflow services can authenticate; all depend on Zitadel tokens.
- Bootstrap must be safe to re-run. Cluster upgrades and local dev cycles both
  require repeated invocations without destroying existing data.
- Credentials produced during bootstrap (service account tokens, OIDC client
  secrets) must be available to services at startup with no manual steps.
- Local development should be fast: changing seed data should not require
  understanding Temporal workflow semantics.
- The same bootstrap binary must work locally (Docker Compose, plain HTTP, h2c
  gRPC) and in production (Kubernetes, TLS).

## Decision

Replace all bootstrap logic with a **standalone `backend/bootstrap` service**
that runs once, performs all seed operations directly via the relevant SDKs and
HTTP APIs, and exits with a zero or non-zero status code.

### Runtime model

The bootstrap binary is the same Go binary as the management service, selected
via environment variables at startup rather than via a separate binary. This
avoids a separate Docker image while keeping the concerns cleanly separated at
the code level.

```sh
PYCK_BOOTSTRAP_ENABLED=true   # enable bootstrap mode
PYCK_BOOTSTRAP_ONLY=true      # exit after bootstrap (don't continue as management)
PYCK_BOOTSTRAP_MODULE=zitadel # which subsystem to bootstrap
```

Three modules run as independent containers, each responsible for one external
system:

| Container | Module | External system |
| --- | --- | --- |
| `bootstrap-zitadel` | `zitadel` | Zitadel org/project/user/app seeding |
| `bootstrap-minio` | `minio` | S3 bucket creation |
| `bootstrap-temporal`* | `temporal` | Temporal namespace registration |

*Temporal bootstrap is optional and can be omitted when Temporal auto-creates
its default namespace.

A synthetic `bootstrap` service in Compose (a no-op `busybox true`) depends on
all three modules with `condition: service_completed_successfully`. Downstream
services declare `depends_on: bootstrap`, so Docker Compose enforces the
required ordering without any custom health-check polling.

In Kubernetes the same containers run as `initContainers` on the management
`Deployment`, or as standalone `Job` resources that must complete before the
main workloads start.

### Configuration: bootstrap.yaml

What gets seeded is declared in a YAML file (`bootstrap.yaml`) that is compiled
into the binary via `go:embed`. An external file can be provided via
`PYCK_BOOTSTRAP_CONFIG_FILE` to override the embedded defaults per environment.

The schema covers:

- **Organizations**: one or more Zitadel organizations, each with projects,
  roles, apps (JWT and OIDC), machine users (PAT and JWT), and human users
- **Exports**: each entity declares where its credentials should be written
  (env file, Kubernetes Secret, or process environment)

Example structure:

```yaml
zitadel:
  organizations:
    - name: Zitadel
      projects:
        - name: Pyck
          roles: [system, admin, writer, reader, temporal_reader, ...]
          apps:
            - name: pyck-frontend
              type: oidc
              oidc_config: { ... }
              exports:
                - type: env
                  file: config/keys/bootstrap.env
                  name: PYCK_FRONTEND_CLIENT_ID
                  field: client_id
          machine_users:
            - username: service-user
              access_token_type: pat
              exports:
                - type: env
                  file: config/keys/bootstrap.env
                  name: PYCK_SERVICE_TOKEN
      human_users:
        - username: bruno
          password: Password1!
          user_grants: [{ project_name: Pyck, role_key: admin }]
```

### Idempotency via credential guards

Every create operation is wrapped by a check-before-create guard
(`backend/common/guards`). Before calling `CreateApp`, for example, the
bootstrapper lists existing apps and skips creation if one with the same name
already exists. This makes every bootstrap run safe to re-run against a live
cluster without duplicating data or failing on conflicts.

OIDC client secrets and PATs are only returned by the Zitadel API on initial
creation. On idempotent re-runs where the resource already exists, the secret
field is absent from the API response; the exporter detects this and skips
writing that field rather than failing.

### Distributed locking

When multiple bootstrap containers start simultaneously (e.g. during a rolling
Kubernetes deployment), only one should execute the seed logic per module. A
PostgreSQL advisory lock is acquired before any seed operations begin:

```text
lock ID = FNV-1a("pyck.ai/bootstrap-<module>") → uint64
```

The lock is held for the duration of bootstrap and released on exit. If another
container is already holding the lock, the newcomer polls until the lock is
released and then verifies the seed is already complete rather than re-running
it. A SQLite table-based fallback is used in tests.

### Credential export

Credentials are written via a pluggable exporter interface with four
implementations:

| Exporter | Use case |
| --- | --- |
| `env` | Appends `KEY=value` lines to a `.env` file on disk |
| `file` | Writes the raw credential (JSON key, PAT token) to a file |
| `process-env` | Sets the credential in the current process's environment so the management service reads it when bootstrap runs in non-exit mode |
| `k8s` | Upserts a Kubernetes Secret in the configured namespace |

Export targets are declared per-entity in `bootstrap.yaml` so each credential
is written exactly where the consuming service expects it.

### gRPC transport

Bootstrap connects to the Zitadel gRPC API without `grpc.WithInsecure()`. The
SDK derives TLS behaviour from the issuer URL scheme: `http://` uses h2c
(cleartext HTTP/2, correct for local Docker Compose), `https://` uses TLS
(correct for production clusters). Previously both the main connection and the
`confirmJWTAccess` connection used `WithInsecure()`, which broke on clusters
where port 443 is TLS because the server received h2c frames on a TLS endpoint
and returned "http2: frame too large" errors.

### Shared connection environment variables

Bootstrap reads the same `ZITADEL_DOMAIN` and `ZITADEL_PORT` environment
variables that all other pyck services use. Previously bootstrap had its own
parallel configuration keys, creating a source of drift when these addresses
changed.

### Settings endpoint in management

The management service exposes a new `GET /static/settings.json` endpoint that
returns OIDC runtime configuration (issuer URL, client IDs) populated during
bootstrap. Frontend applications fetch this at startup to discover their OAuth2
configuration rather than having it hardcoded or injected at build time.

### Reusable infrastructure in common

Two new packages in `backend/common` extract patterns used by bootstrap but
general enough for other services:

- **`common/locking`**: dialect-aware advisory locks (PostgreSQL native,
  SQLite table-based for tests). Useful anywhere a one-time-init or
  leader-election primitive is needed.
- **`common/guards`**: concurrent dependency checks with retry and timeout.
  Services use this to wait for databases and external APIs to become healthy
  before starting.

A new `common/services/kubernetes` package provides a thin Kubernetes client
for Secret reads and upserts, used by the k8s credential exporter.

The Zitadel HTTP client in `common/services/zitadel` is refactored: ~300 lines
of ad-hoc HTTP plumbing are replaced with a typed OAuth2 `TokenSource` that
integrates with the Zitadel SDK's standard auth flow.

## Consequences

### Positive

- **No self-referential dependency.** Bootstrap no longer relies on Temporal to
  bootstrap Temporal. Each module is a simple Go program with no external
  orchestration dependencies.
- **Idempotent by default.** Every run is safe against a live cluster. Partial
  failures leave the cluster in a consistent state; re-running bootstrap
  completes whatever was left.
- **Faster local iteration.** Changing seed data means editing `bootstrap.yaml`
  and restarting one container. No workflow replay, no activity registration, no
  management service restart required.
- **Automated credential distribution.** Service tokens, OIDC client IDs/secrets,
  and app keys are written to env files, Kubernetes Secrets, or the process
  environment automatically. No manual copy-paste between bootstrap output and
  service configuration.
- **Works on real clusters without TLS changes.** The gRPC transport adapts to
  the issuer URL scheme, so the same binary runs locally (h2c) and in production
  (TLS) without configuration changes.
- **~1 100 lines of workflow code removed.** `zitadel-setup`, `temporal-setup`,
  and `bootstrap/roles.go` are deleted from management. The management service
  startup path is simpler and has no bootstrap side-effects when
  `PYCK_BOOTSTRAP_ENABLED=false`.

### Negative

- **Additional build target.** The bootstrap service adds one more container to
  build and push. Currently it shares the management Dockerfile (`SERVICE_NAME`
  build arg); a dedicated `bootstrap` Dockerfile entry will be needed once the
  module fully separates.
- **Ordering dependency in Compose.** All application services depend on the
  synthetic `bootstrap` service completing successfully. If bootstrap fails,
  the entire stack fails to start rather than surfacing the error in individual
  services. This makes the dependency explicit but also means a single failed
  Zitadel API call blocks everything.
- **Credential files on shared volumes.** Credentials are distributed via
  volume-mounted env files locally, and via Kubernetes Secrets in production.
  This requires careful volume mount configuration; a misconfigured path means
  a service starts without its credentials and fails at the first authenticated
  call.
- **OIDC secrets are write-once.** Zitadel only returns OIDC client secrets and
  PATs at creation time. If the credential export fails after creation (e.g.
  disk full, permission error), the secret is lost and the OIDC app must be
  recreated. Operators should verify exported credentials after bootstrap.
- **bootstrap.yaml schema is implicit.** The YAML schema is defined by Go
  structs but not published as a JSON Schema. Errors in `bootstrap.yaml` surface
  at runtime rather than at schema-validation time.
