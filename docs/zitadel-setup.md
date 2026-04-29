# Zitadel Setup

Zitadel v4 splits the authentication platform into two components:

- **Zitadel API** — the Go-based server handling OIDC/OAuth2, gRPC, REST, and the
  Console management UI (`/ui/console`).
- **Zitadel Login** — a standalone Next.js application that renders the Login v2 UI
  (login forms, MFA, registration) and calls the Zitadel Session API server-side.

Both are deployed as separate containers. The login container shares the Zitadel
container's network namespace (`network_mode: service:zitadel`) so it can reach
the API at `localhost:8080` with the correct Host header.

## Network Architecture

```
                      ┌─────────────────────────────────────────────────────────────────┐
                      │                     Docker Network (default)                    │
                      │                                                                 │
  ┌─────────┐  HTTP   │  ┌──────────────────────────────┐                                │
  │ Browser ├─────────┼─▶│ zitadel + zitadel-login      │                                │
  │         │ :8080   │  │ (shared network namespace)   │                                │
  │         │         │  │                              │                                │
  │         │  HTTP   │  │  zitadel  :8080 (API+gRPC)   │◀─── gRPC (h2c) ──────┐         │
  │         ├─────────┼─▶│  login    :3000 (Login v2)   │◀─── HTTP (OAuth) ───┐│         │
  │         │ :3000   │  │                              │                     ││         │
  └─────────┘         │  └──────────────────────────────┘                     ││         │
                      │                                                       ││         │
                      │  ┌────────────────────────────────────────────────┐   ││         │
                      │  │                Backend Services                │   ││         │
                      │  │                                                │   ││         │
                      │  │  inventory ────────────────────────────────────┼───┘│         │
                      │  │  main-data ────────────────────────────────────┼────┤         │
                      │  │  management ───────────────────────────────────┼────┤         │
                      │  │  file ─────────────────────────────────────────┼────┤         │
                      │  │  picking ──────────────────────────────────────┼────┤         │
                      │  │  receiving ────────────────────────────────────┼────┤         │
                      │  │  workflow ─────────────────────────────────────┼────┤         │
                      │  │  temporal ─────────────────────────────────────┼────┘         │
                      │  │                                                │               │
                      │  └────────────────────────────────────────────────┘               │
                      │                                                                   │
                      │  ┌──────────────────┐                                             │
                      │  │ bootstrap-zitadel├──── gRPC (h2c) + HTTP (OAuth) ──▶ zitadel   │
                      │  └──────────────────┘                                             │
                      └───────────────────────────────────────────────────────────────────┘

  No TLS — plain HTTP everywhere. Browsers treat localhost as a "secure context",
  so Secure cookies work without HTTPS.
```

All traffic is plain HTTP. Zitadel runs with `--tlsMode disabled` and
`EXTERNALSECURE=false`. This works for local development because browsers treat
`localhost` as a secure context, allowing Secure cookies over plain HTTP.

## Environment Variables

Backend services use three Zitadel-related addresses:

```
  PYCK_ZITADEL_AUDIENCE                PYCK_ZITADEL_OAUTH_URL              PYCK_ZITADEL_GRPC_ADDR
  http://localhost:8080                http://localhost:8080                localhost:8080
  ──────────────────────────────────   ──────────────────────────────────   ──────────────────────────
           │                                       │                                  │
           ▼                                       ▼                                  ▼
  ┌─────────────────┐              ┌──────────────────────────┐          ┌──────────────────────┐
  │   JWT "aud"     │              │  Token exchange endpoint │          │   gRPC dial target   │
  │   claim value   │              │                          │          │                      │
  │                 │              │  POST /oauth/v2/token    │          │  SDK management and  │
  │  Must match the │              │  POST /oauth/v2/         │          │  admin API calls     │
  │  Zitadel OIDC   │              │       introspect         │          │                      │
  │  issuer URL     │              │                          │          │  host:port only,     │
  │                 │              │                          │          │  no scheme — the     │
  │  Also used for  │              │                          │          │  SDK adds grpc://    │
  │  deterministic  │              │                          │          │  or grpcs:// based   │
  │  UUID compute   │              │                          │          │  on TLS_INSECURE     │
  └─────────────────┘              └──────────────────────────┘          └──────────────────────┘
```

| Variable | Value (local) | Used by | Purpose |
|----------|---------------|---------|---------|
| `PYCK_ZITADEL_AUDIENCE` | `http://localhost:8080` | JWT assertions, UUID computation | OIDC issuer / JWT `aud` claim |
| `PYCK_ZITADEL_OAUTH_URL` | `http://localhost:8080` | `http_client.go`, `token_source.go` | Token exchange and introspection |
| `PYCK_ZITADEL_GRPC_ADDR` | `localhost:8080` | `sdk_client.go`, `bootstrap.go` | gRPC connection target |
| `PYCK_ZITADEL_TLS_INSECURE` | `true` | SDK client, HTTP client | Use h2c for gRPC, skip TLS verify |
| `PYCK_ZITADEL_APP_KEYFILE` | `/data/keys/local-key.json` | All services | JWT profile key for token exchange |
| `PYCK_ZITADEL_SERVICE_KEYFILE` | `/data/keys/zitadel-admin-sa.json` | Bootstrap, management | Admin service account key |

### Why `localhost` as ExternalDomain?

Zitadel v4 uses the HTTP `Host` header for instance routing. The login container
shares the Zitadel container's network namespace (`network_mode: service:zitadel`),
so `localhost:8080` reaches Zitadel directly with `Host: localhost` — matching
the configured `EXTERNALDOMAIN`.

## Init and Setup

Zitadel uses a two-phase database initialization before the API server can start.

### `zitadel init`

Runs **once** over the entire Zitadel lifecycle. Uses the **privileged** database user
(`POSTGRES_ADMIN_USERNAME`) to:

- Create the `zitadel` database if it doesn't exist
- Create the unprivileged `zitadel` database user if it doesn't exist

This is pure database bootstrapping — it only touches PostgreSQL roles and databases.

### `zitadel setup`

Runs on **every version upgrade**. Uses the **unprivileged** user created by init to:

- Create and migrate projection tables (the read-model for Zitadel's CQRS architecture)
- Run data migrations for the new version
- With `--init-projections=true`: initialize projections like **web keys**

The `--init-projections=true` flag is required because Zitadel v4 introduced web keys
as a projection. Without it, no active web key exists and Zitadel cannot sign tokens.

### Upgrading Across Major Versions

`zitadel setup` applies all pending migrations incrementally, so upgrading is:

1. Stop the Zitadel API server
2. **Back up PostgreSQL** — setup modifies schema and data in place with no rollback
3. Update the container image to the new version
4. Run `zitadel setup --init-projections=true`
5. Start the API server

Zitadel does not officially support skipping major versions. For a v2→v4 jump, the
safer path is v2→v3→v4 (update, run setup, repeat). Check the
[release notes](https://github.com/zitadel/zitadel/releases) for breaking migration
notes between versions.

### Web Keys

Web keys are RSA/EC key pairs managed by Zitadel for OIDC token operations:

- **Signing** — the active private key signs all new JWTs (ID tokens, access tokens)
- **JWKS endpoint** (`/.well-known/jwks.json`) — public keys are served here so
  applications can locally verify tokens without calling Zitadel

There is always exactly one active web key per instance (a Zitadel instance is a
logical tenant — an isolated identity provider with its own domain, users, projects,
and settings, not a container or cluster node; our local setup has one instance on
`localhost`). Activating a new key automatically deactivates the previous
one. Old public keys are kept on the JWKS endpoint long enough to cover token validity
periods (~24h for access/ID tokens, ~3 months for `id_token_hint`).

## Container Dependency Graph

```
                        ┌─────────────────────────────────────────────┐
                        │            Phase 1 — Database               │
                        │                                             │
                        │              db (PostgreSQL)                │
                        │                    │                        │
                        └────────────────────┼────────────────────────┘
                                             │ healthy
                        ┌────────────────────┼────────────────────────┐
                        │            Phase 2 — Schema Setup           │
                        │                    │                        │
                        │                    ▼                        │
                        │             zitadel-init                    │
                        │        creates database schema              │
                        │           and user accounts                 │
                        │              (runs once)                    │
                        │                    │                        │
                        │                    │ completed              │
                        │                    ▼                        │
                        │            zitadel-setup                    │
                        │         runs database migrations            │
                        │      initializes projections (web keys)     │
                        │              (runs once)                    │
                        │                    │                        │
                        └────────────────────┼────────────────────────┘
                                             │ completed
                        ┌────────────────────┼────────────────────────┐
                        │            Phase 3 — API Server             │
                        │                    │                        │
                        │                    ▼                        │
                        │               zitadel                       │
                        │       OIDC/OAuth2 API + Console UI          │
                        │          http://:8080 (plain HTTP)          │
                        │          --tlsMode disabled                 │
                        │            (stays running)                  │
                        │                    │                        │
                        └────────────────────┼────────────────────────┘
                                             │ healthy
                        ┌────────────────────┼────────────────────────┐
                        │            Phase 4 — Bootstrap              │
                        │                    │                        │
                        │                    ▼                        │
                        │          bootstrap-zitadel                  │
                        │    creates orgs, users, projects, roles     │
                        │    generates PATs → config/keys/            │
                        │              (runs once)                    │
                        │                    │                        │
                        └────────────────────┼────────────────────────┘
                                             │ completed
                        ┌────────────────────┼────────────────────────┐
                        │            Phase 5 — Login UI               │
                        │                    │                        │
                        │                    ▼                        │
                        │            zitadel-login                    │
                        │       Next.js Login v2 application          │
                        │   network_mode: service:zitadel             │
                        │   (shares zitadel's network namespace)      │
                        │   http://:3000 (via zitadel's ports)        │
                        │                                             │
                        └─────────────────────────────────────────────┘
```

Each phase waits for the previous one to complete or become healthy before starting.
The `bootstrap` gate container (not shown) waits for all of the above to be ready
before `task init` exits successfully.

## Login Service User

The login app authenticates to the Zitadel Session API using a **Personal Access
Token (PAT)** belonging to a dedicated machine user. This is set up by the bootstrap
process.

### How it is seeded

The bootstrap configuration in `backend/bootstrap/pkg/bootstrap/bootstrap.yaml`
defines the login service user:

```yaml
machine_users:
  - username: login-service-user
    name: Login Service User
    access_token_type: pat
    role:
      - IAM_LOGIN_CLIENT
    exports:
      - type: file
        file: login-client.pat
```

The bootstrap container (`bootstrap-zitadel`) performs these steps:

1. **Creates the machine user** `login-service-user` via the Zitadel Management API.
2. **Generates a PAT** for the user and writes it to `config/keys/login-client.pat`.
3. **Assigns the `IAM_LOGIN_CLIENT` role** at the instance (IAM) level. This is an
   instance-level membership, not a project-level grant — it allows the user to
   manage authentication sessions on behalf of any user.

The bootstrap is idempotent: on subsequent runs it detects existing resources and
skips creation, but always regenerates PATs.

### How it is connected

The login container mounts the PAT file and reads it at startup:

```yaml
zitadel-login:
  network_mode: service:zitadel
  environment:
    - ZITADEL_API_URL=http://localhost:8080
    - ZITADEL_SERVICE_USER_TOKEN_FILE=/data/keys/login-client.pat
  volumes:
    - config/keys/:/data/keys/:ro
```

This is why `zitadel-login` depends on `bootstrap-zitadel` completing — the PAT file
must exist before the login app starts.

### Authentication flow

```
Browser                      Zitadel API (:8080)           Login UI (:3000)
   │                              │                              │
   │── GET /oauth/v2/authorize ──▶│                              │
   │                              │── 302 redirect ─────────────▶│
   │◀─────────────────────────────────── login page ─────────────│
   │                              │                              │
   │── POST credentials ─────────────────────────────────────────▶│
   │                              │◀── create session (PAT) ─────│
   │                              │── session result ────────────▶│
   │◀──────────────────────────────────── 302 callback ──────────│
   │                              │                              │
   │── GET /login/callback ──────▶│                              │
   │◀── 302 + authorization code ─│                              │
   │                              │                              │
   │── POST /oauth/v2/token ─────▶│                              │
   │◀── access token + id token ──│                              │
```

The Login UI never sees user credentials directly in its responses — it forwards
them to the Zitadel Session API using the PAT for authentication. The PAT identifies
the login app as a trusted client with the `IAM_LOGIN_CLIENT` role, which grants
permission to create and verify authentication sessions.

## Custom Token Source

The Zitadel Go SDK's `WithJWTProfileTokenSource` uses OIDC discovery internally:
it fetches `{issuer}/.well-known/openid-configuration` and validates that the
returned `issuer` matches. When the internal URL differs from the external issuer,
this validation can fail.

To handle this, the SDK client uses a custom `oauth2.TokenSource` (see
`backend/common/services/zitadel/token_source.go`) that:

1. Reads the service key file and creates a JWT with `aud` set to the issuer URL.
2. POSTs the assertion directly to the OAuth endpoint
   (`http://localhost:8080/oauth/v2/token`) — no OIDC discovery needed.
3. Caches the access token and refreshes it on expiry.

## Key Files

| File | Purpose |
|------|---------|
| `config/compose/third-party/zitadel.yaml` | Compose definitions for all Zitadel containers |
| `config/compose/backend/bootstrap.yaml` | Bootstrap container definitions and gate |
| `config/zitadel/compose.env` | Zitadel environment variables (database, external domain) |
| `backend/bootstrap/pkg/bootstrap/bootstrap.yaml` | Bootstrap seed data (users, roles, PATs) |
| `backend/bootstrap/internal/zitadel/bootstrap.go` | Bootstrap implementation (user creation, IAM roles) |
| `backend/bootstrap/internal/zitadel/config.go` | Bootstrap configuration structs |
| `backend/common/services/zitadel/token_source.go` | Custom OAuth2 token source (bypasses OIDC discovery) |
| `backend/common/services/zitadel/http_client.go` | HTTP client for token introspection |
| `backend/common/services/zitadel/sdk_client.go` | gRPC SDK client (management + admin APIs) |
| `config/keys/login-client.pat` | Generated PAT for login service user (not committed) |

## Local URLs

| URL | Service |
|-----|---------|
| `http://localhost:8080` | Zitadel API + Console UI |
| `http://localhost:8080/ui/console` | Zitadel management console |
| `http://localhost:3000/ui/v2/login` | Login v2 UI |

Default admin login: `administrator` / `Password1!`
