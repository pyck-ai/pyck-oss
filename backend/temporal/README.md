# Temporal + ZITADEL Local Development Setup

This guide describes how to run the Temporal + ZITADEL integrated setup locally using Docker Compose. This setup includes:

- ZITADEL with TLS enabled and a pre-configured machine user
- Temporal backend with a separate custom-auth frontend
- Temporal UI configured for OIDC login via ZITADEL

## Prerequisites

- [Docker](https://www.docker.com/products/docker-desktop) installed
- [Docker Compose](https://docs.docker.com/compose/install/) installed

## Steps to Run

1. **Run Local Setup Script**

Before starting the containers, run the `local-setup` script to initialize ZITADEL organization, project, and tenants:

```bash
task local-setup
```

This will:

- Create a ZITADEL org, project, and machine user
- Generate a local key file for Temporal auth
- Populate the `.env` file with required variables

At the end of the script execution, you will see a message like:

```
>>> Login at http://localhost:8080 with username 'zitadel-admin@localhost' and password 'Password1!'
```

2. **Login to ZITADEL and Assign Roles**

Open your browser and go to:

```
http://localhost:8080
```

Login with the credentials provided in the script output. Then:

- Navigate to **ZITADEL organizations** → **Projects** → **PYCK** → **Grants**
- Select the `local-dev` project member
- Add the following roles:
  - `temporal_reader`
  - `temporal_writer`

3. **Create Zitadel `temporal_auth` project**

- Navigate to **ZITADEL organizations** → **local-dev** → **Projects** → **New Project**
- Name it `temporal_auth`
- On the project creation page, enable the following checkboxes:
  - Assert Roles on Authentication
  - Check Authorization on Authentication
  - Check for Project on Authentication
- Click **Save**

Then:

- Click **New Application** → choose **Web**
- Name the application `temporal_auth_app`
- In the **Code** tab, fill in the following:
  - **Redirect URI**: value of `TEMPORAL_AUTH_CALLBACK_URL` from `docker-compose.yml`
  - **Post Logout Redirect URI**: value of `TEMPORAL_UI_BASE_URL` from `docker-compose.yml`
- Save the application
- Copy the **Client ID** and **Client Secret** and update them in `docker-compose.yml` under `temporal-ui-auth` service:
  - `TEMPORAL_AUTH_CLIENT_ID`
  - `TEMPORAL_AUTH_CLIENT_SECRET`

4. **Configure Token Settings**

- Go to the **Token Settings** tab in the `temporal_auth` project
- Set **Auth Token Type** to `JWT`
- Enable all checkboxes on the page

5. **Create a User for Login**

- In ZITADEL, go to **Users** → **Add User**
- Create a user account and assign the role `temporal_writer` to the user

6. **Build Custom Services**

```bash
docker compose build
```

This builds `temporal-auth` and `temporal-ui-auth` services.

7. **Start the Stack**

```bash
docker compose up -d
```

8. **Access the Services**

- **ZITADEL Admin Console**: [http://localhost:8080](http://localhost:8080)
- **Temporal UI (OIDC)**: [http://localhost:8083](http://localhost:8083)

9. **Run Tests**

Before running tests, ensure that your `.env` file contains the following variables:

```
TEMPORAL_TEST_AUTH_TOKEN=<token-for-tests>
TEMPORAL_TEST_NAMESPACE=<namespace-used-in-tests>
TEMPORAL_TEST_AUTH_ADDRESS=<temporal_address>
```

Then run the tests using:

```bash
task test
```



10. **Public APIs (no token required)**

These APIs are always allowed:

- `/grpc.health.v1.Health/Check`
- `/temporal.api.workflowservice.v1.WorkflowService/GetSystemInfo`

---

11. **APIs allowed with any valid JWT that includes at least one namespace claim**

- `/temporal.api.workflowservice.v1.WorkflowService/GetClusterInfo`
- `/temporal.api.workflowservice.v1.WorkflowService/ListNamespaces`
- `/temporal.api.operatorservice.v1.OperatorService/ListNexusEndpoints`

---

12. **Namespace-based authorization**

All other APIs require:

- A valid JWT with `claims.Namespaces`
- A matching namespace (from `target.Namespace` or `GetNamespace()`)
- A role of `READER` or `WRITER` for that namespace

Access is denied if any condition is missing.

13. **Explicitly denied**

These APIs are always denied, regardless of role:

- `DeleteNamespace`
- `UpdateNamespace`

---

## Choosing the server roles

`temporal-server start` decides which Temporal services (frontend,
internal-frontend, history, matching, worker) a process runs. First match wins:

| Order | Source | Notes |
|-------|--------|-------|
| 1 | `--service` / `--svc` flag, or `TEMPORAL_SERVICES` env | Repeat the flag or comma separate: `TEMPORAL_SERVICES=history,matching` |
| 2 | `--services` flag (deprecated, hidden) | Comma separated. Logs a deprecation warning |
| 3 | Unset | Every service declared in the loaded config (embedded template: frontend, matching, history, worker, plus internal-frontend when `USE_INTERNAL_FRONTEND` is set) |

Entries are trimmed, deduplicated and sorted. Startup fails when a name is not
a Temporal service, or when it is not declared in the loaded config (the
config carries the service's ports), for example `internal-frontend` without
`USE_INTERNAL_FRONTEND`. The resolved list is logged at startup
(`starting temporal services`).

The local compose stack sets none of these, so its single `temporal` container
keeps running every service. Per-role deployments set `--service=<role>` or
`TEMPORAL_SERVICES=<role>`. Code: `cmd/server/services.go`.

---

## Roles

```go
ROLE_TEMPORAL_READER
ROLE_TEMPORAL_WRITER
```

---

## Workflow state-change events

The server turns every workflow status change into a state-change event on
the NATS stream (consumed by the workflow service's signal router). The
`PYCK_EVENT_ADAPTER` setting picks the source: `default` / `postgres_listen`
(PostgreSQL LISTEN/NOTIFY on the visibility store) or `grpc`.

| Variable | Default | Meaning |
|---|---|---|
| `PYCK_EVENT_ADAPTER_SERVICES` | empty | Comma-separated Temporal services (`frontend`, `internal-frontend`, `history`, `matching`, `worker`) whose process runs the LISTEN adapter. Empty runs it in every process. An unknown name, or a role whose local `TEMPORAL_ADDRESS` it does not serve, is a startup error. Ignored by the `grpc` adapter. |

Every process that listens receives every notification and publishes the
event. The publish carries a message ID, so JetStream keeps one copy within
its 2 minute duplicate window (`events.StreamDuplicateWindow`), but each extra
listener is still wasted work. In a deployment with separate pods per role
set `PYCK_EVENT_ADAPTER_SERVICES=internal-frontend`. The setting is matched
against the services the process starts, as resolved from `--service` /
`TEMPORAL_SERVICES` (see "Choosing the server roles").

The listener dials `TEMPORAL_ADDRESS` (image default `:7236`, the
internal-frontend) on its own pod, so it must run in a role that serves that
address; `history` does not. A local address (empty host, `localhost`,
loopback, `0.0.0.0`) that no running frontend or internal-frontend serves
fails startup when the variable names this role, and with the variable empty
such a role skips the listener (Info log). A non-local address is not checked.

Listening on fewer processes narrows the redundancy, and PostgreSQL NOTIFY is
not stored: the listener is fire-and-forget. A change made while no process
listens is lost. The subscribed workflow is never started or signalled and
nothing retries it. Known gaps:

- Startup: the Temporal server starts before LISTEN is established, and the
  adapter retries until `PYCK_EVENT_ADAPTER_POSTGRES_CONNECT_TIMEOUT`
  (default 120s). Changes in that window are lost, and if the timeout
  expires the server logs the error and keeps running without a listener.
- Reconnects: `pq.Listener` reconnects after a lost connection. Notifications
  sent in between are lost. The adapter only logs the reconnect and does not
  reconcile afterwards.
- Shutdown: `Stop` stops the adapter before the Temporal server, so changes
  during the server's own drain are not seen by that pod.
- Database failover or restart: every listener drops at once, so extra
  replicas do not help.
- Readiness: the health check only probes the frontend gRPC service. A pod
  whose listener never started or died stays Ready.

Run at least 2 listening replicas and roll them with `maxUnavailable: 0` or a
PodDisruptionBudget that keeps one up.
