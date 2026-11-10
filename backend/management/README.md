# Management service

### Bootstrap Env
```
PYCK_TEMPORAL_URL
```

### Env
```
PYCK_DATABASE_MASTER_URL
PYCK_DATABASE_SLAVE_URL
PYCK_DATABASE_DEBUG -> default value: false
PYCK_DATABASE_DRIVER -> default value: "postgres"
PYCK_DYNAMIC_SCHEMA_CHECKS
PYCK_TEMPORAL_URL
PYCK_TEMPORAL_DIAL_TIMEOUT -> default value: 30s
PYCK_ZITADEL_AUDIENCE
PYCK_ZITADEL_ORG_ID
PYCK_ZITADEL_PROJECT_ID
PYCK_ZITADEL_APP_KEYFILE
PYCK_ZITADEL_SERVICE_KEYFILE
PYCK_ZITADEL_TLS_INSECURE
PYCK_NATS_URL
PYCK_NATS_STREAM_NAME
PYCK_NATS_TENANT_ACCOUNT_NAME -> default value: pyck-tenants (account of tenant clients; if the NATS server config does not declare it, every token connection is refused; if it is the services' account, or the NATS server has no system account answering $SYS.REQ.USER.INFO, startup fails)
PYCK_NATS_WS_URL
PYCK_NATS_REPLICAS_NO
PYCK_NATS_STREAM_MAX_AGE -> default value: 72h (every event stream; 0 or at least 2m, see Event streams)
PYCK_NATS_TENANT_STREAM_MAX_MSGS -> default value: 100000 (positive, as are the next two)
PYCK_NATS_SYSTEM_STREAM_MAX_MSGS -> default value: 200000
PYCK_NATS_AGGREGATE_STREAM_MAX_MSGS -> default value: 2000000
PYCK_EVENT_STREAM_LOCK_WAIT -> default value: 10s (registerTenant/restoreTenant wait for the stream lock; positive)
PYCK_EVENT_STREAM_RECONCILE_CONCURRENCY -> default value: 32 (parallel stream writes of the reconcile and repair; positive)
PYCK_SERVICE_TOKEN
PYCK_TX_RETRIES -> default value: 50
PYCK_DISCOVERY_ENVIRONMENT (optional; published as meta.environment in /.well-known/pyck-configuration, the name in the DNS TXT record _pyck.pyck.cloud; default: PYCK_ENV)
PYCK_UI_STORE_URL (optional; published in /.well-known/pyck-configuration)
PYCK_CLI_CLIENT_ID (optional; published in /.well-known/pyck-configuration, no value = client omitted)
PYCK_MOBILE_CLIENT_ID (optional; same)
PYCK_BRUNO_CLIENT_ID (optional; same)
PYCK_MCP_CLIENT_ID (optional; same)
```

### Client discovery

`GET /.well-known/pyck-configuration` is unauthenticated and describes the environment for its clients. Every key is
kebab-case. The document has three sections:

- `meta.environment`: the environment's name as in the DNS TXT record `_pyck.pyck.cloud` (`dev`, `test`, `demo`, `eu`).
  It is `PYCK_DISCOVERY_ENVIRONMENT`, or `PYCK_ENV` when that is empty (eu-prod sets `PYCK_DISCOVERY_ENVIRONMENT=eu`).
- `endpoints`: each `{ "url": ... }` for `auth`, `app`, `graphql`, and, when configured, `worker-api`, `ui-store`,
  `feedback`, `barcodes` and `otel` (`otel` also carries `key`). `auth.url` is the OIDC issuer and equals
  `PYCK_FRONTEND_AUTH_URL` verbatim.
- `components`: per client, `pyck-control`, `pyck-cli`, `pyck-mobile`, `pyck-bruno` and `pyck-mcp`, each with
  `auth.client-id` (`pyck-control` also `redirect-uri`). A client without a client ID is omitted; `pyck-control` always
  has `extra` with `build`, `debug` and `features` (its keys converted to kebab-case, `{}` when the value is not a JSON
  object).

It is built once at startup from `PYCK_FRONTEND_*`, `PYCK_WORKER_API_URL` and the variables above. Empty optional values
are omitted. `version` (1) covers the whole document and bumps only when a field is removed or renamed; adding a field
does not. `/static/settings.json` stays as is (camelCase) for the current frontend.

### Event streams

Management owns the JetStream layout (`backend/common/events/streams.go`). `<stream>` is `PYCK_NATS_STREAM_NAME`.

| Stream | Subjects | Written by | Read by |
|---|---|---|---|
| `<stream>-<tenantID>` | `<stream>.<tenantID>.>` | services; that tenant's clients | that tenant's clients (auth callout) |
| `<stream>-system` | `<stream>-system.>` (custom events, state changes of non-tenant namespaces) | services | services, via the aggregate |
| `<stream>` (aggregate) | none; sources every stream above, duplicate detection off | the sources | services' consumers |

- Message IDs deduplicate per stream, so a tenant can only suppress messages in its own stream.
- At startup management creates every active tenant's stream and migrates the shared stream of the old layout into
  the aggregate in place, keeping its messages and consumers (`tenants.ReconcileStreams`). A tenant stream that fails,
  or an aggregate past `max_payload`, is logged without failing startup; any other failure, such as the system stream,
  stops it. Soft-deleted tenants get no stream there: `restoreTenant` puts it, and an existing one stays sourced.
- Every tenant-reconcile sweep (`PYCK_TENANT_RECONCILE_INTERVAL`) puts the streams active tenants are missing
  (`tenants.RepairStreams`), so one lost after startup does not wait for a restart. The gauge
  `management_tenant_event_streams_missing` counts the active tenants whose stream the last reconcile or repair could
  not put, or the aggregate does not source (past `max_payload`). A sweep runs on one replica: alert on the replica
  with the newest `management_tenant_event_streams_checked_timestamp_seconds`.
- `registerTenant` puts the stream before it commits the pending row, whose create event goes there; a row that is
  not inserted gets its stream deleted again. A failed registration keeps its row, and with it its stream.
  `StreamTopology.DeleteTenant` removes a stream and purges the tenant from the aggregate; the hard delete (#1291, not
  built yet) must call it too. Changes run under a Postgres advisory lock, since the aggregate's source list is
  written whole. `registerTenant` and `restoreTenant` wait for it at most `PYCK_EVENT_STREAM_LOCK_WAIT` (e.g. behind a
  startup Reconcile), then fail without creating anything; a failed registration's stream delete waits as long, then
  leaves the empty stream.
- Every event is stored twice (its stream and the aggregate), which halves the history a file store holds. Caps count
  messages (`PYCK_NATS_*_STREAM_MAX_MSGS`, default tenant 100k, system 200k, aggregate 2M, all
  `PYCK_NATS_STREAM_MAX_AGE` 3 days, 0 or at least the 2m duplicate window); the oldest events go first, so one tenant
  publishing fast enough pushes other tenants' unread events out of the aggregate. Management's
  values apply: the other services use theirs only to create a missing system stream or aggregate. No stream sets `MaxBytes`: the server reserves it in
  `max_file_store` when the stream is created, so the local 256MB store could not even hold the system stream. A bound in bytes needs account JetStream limits in the deployment.
- The aggregate's update request lists every source (~53 bytes each, ~0.5KB base) and must fit the server's
  `max_payload`: about 2,400 tenants at the local 128KB, 19,700 at the default 1MB. Management warns at 80% and refuses
  a new tenant past it (`events.ErrAggregateTooLarge`); the streams left unsourced count in the gauge above.
