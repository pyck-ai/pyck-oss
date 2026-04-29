# Bootstrapping

The bootstrap process initializes external dependencies before the management service starts. Each dependency runs as a separate init container in Docker Compose and exits after completion.

## Modules

| Module | Container | Description |
|---|---|---|
| `zitadel` | `zitadel-bootstrap` | Seeds organizations, users, projects, roles, and exports credentials |
| `temporal` | `temporal-bootstrap` | Creates the Temporal namespace and registers setup workflows |
| `minio` | `minio-bootstrap` | Ensures the configured S3 bucket exists |

Each module is selected via the `PYCK_BOOTSTRAP_MODULE` environment variable.

## How It Works

When a bootstrap container starts, it:

1. Loads the bootstrap configuration (embedded or from file)
2. Waits for the database to become healthy
3. Acquires a database advisory lock (per module) to prevent concurrent runs
4. Executes the selected bootstrap module
5. Exits (when `PYCK_BOOTSTRAP_ONLY=true`)

The advisory lock uses PostgreSQL `pg_try_advisory_lock` or a SQLite table-based equivalent, selected automatically based on the database dialect.

## Environment Variables

### Bootstrap Control

| Variable | Default | Description |
|---|---|---|
| `PYCK_BOOTSTRAP_ENABLED` | `true` | Enables the bootstrap process. Set to `false` on the regular management service to skip bootstrapping entirely. |
| `PYCK_BOOTSTRAP_ONLY` | `true` | When `true`, the process exits after bootstrapping completes. When `false`, the service continues running as the management API after bootstrap. Use `true` for the dedicated bootstrap containers. |
| `PYCK_BOOTSTRAP_MODULE` | — | Selects which module to bootstrap. Valid values: `zitadel`, `temporal`, `minio`. Required when `PYCK_BOOTSTRAP_ENABLED=true`. |
| `PYCK_BOOTSTRAP_MODE` | — | Legacy flag to trigger bootstrap mode. Prefer `PYCK_BOOTSTRAP_ENABLED` instead. |
| `PYCK_BOOTSTRAP_TIMEOUT` | `5m` | Maximum duration for the entire bootstrap process (e.g. `10m`). The bootstrap context is cancelled after this timeout, preventing indefinite hangs if a dependency is unresponsive. |

### Bootstrap Configuration

| Variable | Default | Description |
|---|---|---|
| `PYCK_BOOTSTRAP_CONFIG_FILE` | — | Path to an external `bootstrap.yaml` file. When set, the configuration is loaded from this file instead of the embedded default. This is useful for customizing the seed data per environment (e.g. different users, roles, or export targets). When unset, the compiled-in `bootstrap.yaml` is used. |

### Zitadel Bootstrap

| Variable | Default | Description |
|---|---|---|
| `PYCK_BOOTSTRAP_ZITADEL_ISSUER` | `http://localhost:8080` | OIDC issuer URL of the Zitadel instance. Used for token exchange and JWT profile authentication. Override this when connecting to a non-local Zitadel deployment. |
| `PYCK_BOOTSTRAP_ZITADEL_API` | `localhost:8080` | gRPC API endpoint of the Zitadel instance. Used for all Admin and Management API calls. Override this when connecting to a non-local Zitadel deployment. |
| `PYCK_BOOTSTRAP_ZITADEL_KEY_PATH` | `/data/keys` | Directory where Zitadel key files are read from and written to. This path must be accessible and is shared via a volume mount. The Zitadel admin service account key (`zitadel-admin-sa.json`) must be present here before bootstrap runs. |
| `PYCK_BOOTSTRAP_ZITADEL_ENV_PATH` | `/data/env` | Directory where `.env` files with exported credentials (e.g. service tokens) are written. This is typically mounted to the project root so that `.env.local` is available to other services after bootstrap. |
| `PYCK_BOOTSTRAP_ZITADEL_K8S_NAMESPACE` | — | Kubernetes namespace for storing exported credentials as K8s secrets. Only relevant in Kubernetes deployments. |
| `PYCK_BOOTSTRAP_ZITADEL_K8S_SECRET_NAME` | — | Name of the Kubernetes secret to store credentials in (e.g. `pyck-secrets`). Only relevant in Kubernetes deployments. |
| `PYCK_BOOTSTRAP_ZITADEL_K8S_IN_CLUSTER` | — | Whether the bootstrap process runs inside a Kubernetes cluster. When `true`, uses in-cluster authentication. |
| `PYCK_BOOTSTRAP_ZITADEL_K8S_CONFIG_PATH` | — | Path to the kubeconfig file for out-of-cluster Kubernetes access. Defaults to `$HOME/.kube/config`. |

## Deprecations

| Item | Replacement | Notes |
|---|---|---|
| `-bootstrap` CLI flag | `PYCK_BOOTSTRAP_ENABLED=true` | The `-bootstrap` flag on the management binary is deprecated and will be removed in a future release. Use the environment variable instead. |
| `PYCK_BOOTSTRAP_MODE` env var | `PYCK_BOOTSTRAP_ENABLED` | Legacy variable kept for backward compatibility. |

## Default Bootstrap Configuration

The default bootstrap configuration is embedded in the binary at compile time via `go:embed` from [`bootstrap.yaml`](pkg/bootstrap/bootstrap.yaml). This file defines the initial Zitadel seed data including:

- **Organizations**: `Zitadel` (primary) and `localDev` (for local development)
- **Human Users**: An administrator account for Zitadel management
- **Machine Users**: Service accounts with PAT tokens (`service-user`, `service-worker-user`, `api-user`)
- **Projects & Roles**: The `Pyck` project with system, admin, writer, reader, and temporal roles
- **Credential Exports**: Keys written to files, tokens written to `.env.local`, and process environment variables set at runtime

To override the default configuration, set `PYCK_BOOTSTRAP_CONFIG_FILE` to point to a custom YAML file with the same schema.

## Volume Mounts (Docker Compose)

The `zitadel-bootstrap` container requires several volume mounts to function correctly:

| Mount | Container Path | Purpose |
|---|---|---|
| `config/keys/` | `/data/keys/` | Shared key directory — Zitadel admin key is read from here, generated service keys are written here |
| Project root (`../../../`) | `/data/env/` | Project root directory — `.env.local` with exported tokens is written here for other services to consume |
| `config/bootstrap.yaml` | `/data/config/bootstrap.yaml` | Optional external bootstrap config file (used when `PYCK_BOOTSTRAP_CONFIG_FILE` is set) |

The `minio-bootstrap` and `temporal-bootstrap` containers use `env_file:` to load environment variables directly and do not require additional volume mounts beyond the base `.env` files.

## Zitadel

The Zitadel bootstrapper connects to the Zitadel instance using the admin service account key and seeds the configured organizations, users, projects, roles, and applications. Generated credentials (API keys, PATs) are exported via the configured exporters (file, env, process env, or Kubernetes secrets).

### Authentication Entities

| Entity Type | What It Represents | Authentication Method | Use Case |
|---|---|---|---|
| App | OAuth2/OIDC Client Application registered in a project | JWT Keys (app key) via `AddAppKey()` | Application that authenticates as itself (client credentials flow). Your API gateway/service. |
| Machine User (PAT) | Service Account. Non-human user account, `AccessTokenType = 0` | PAT (Personal Access Token) via `AddPersonalAccessToken()` | Automated service that acts as a user with specific roles. Simple token-based auth. |
| Machine User (JWT) | Service Account. Non-human user account, `AccessTokenType = 1` | JWT Keys (machine key) via `AddMachineKey()` | Automated service that acts as a user with specific roles. Uses cryptographic signing (more secure). |
