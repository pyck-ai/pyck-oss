# Workflow service

Why subscriptions, routing and delivery work the way they do: [ADR-0002](../../docs/adr/0002-workflow-event-routing-and-delivery.md).

### Env variables
```
PYCK_DATABASE_MASTER_URL
PYCK_DATABASE_SLAVE_URL
PYCK_DATABASE_DEBUG -> default value: false
PYCK_DATABASE_DRIVER -> default value: "postgres"
PYCK_GATEWAY_URL
PYCK_TEMPORAL_URL
PYCK_TEMPORAL_DIAL_TIMEOUT -> default value: 30s
PYCK_TEMPORAL_CLIENT_CREATION_TIMEOUT -> default value: 30s
PYCK_ZITADEL_AUDIENCE
PYCK_ZITADEL_ORG_ID
PYCK_ZITADEL_PROJECT_ID
PYCK_ZITADEL_APP_KEYFILE
PYCK_ZITADEL_TLS_INSECURE
PYCK_NATS_URL
PYCK_NATS_STREAM_NAME
PYCK_NATS_WS_URL
PYCK_NATS_REPLICAS_NO
PYCK_SERVICE_TOKEN
PYCK_TX_RETRIES -> default value: 50
PYCK_WORKFLOW_SUBSCRIPTION_TTL -> default value: 1h
PYCK_WORKFLOW_SUBSCRIPTION_JANITOR_INTERVAL -> default value: 5m
PYCK_WORKFLOW_ROUTER_ACK_WAIT -> default value: 60s
PYCK_WORKFLOW_ROUTER_MAX_ACK_PENDING -> default value: 256
PYCK_WORKFLOW_ROUTER_MAX_DELIVER -> default value: 20
PYCK_WORKFLOW_ROUTER_NAK_BACKOFF -> default value: 1s,5s,30s,2m,5m
PYCK_WORKFLOW_ROUTER_CONCURRENCY -> default value: 16
PYCK_WORKFLOW_ROUTER_HEALTH_INTERVAL -> default value: 5s
PYCK_WORKFLOW_ROUTER_HEALTH_TIMEOUT -> default value: 2s
PYCK_WORKFLOW_ROUTER_RESUME_MIN_INTERVAL -> default value: 1s
PYCK_WORKFLOW_ROUTER_RESUME_MAX_INTERVAL -> default value: 5s
PYCK_WORKFLOW_ROUTER_PAUSED_WARN_INTERVAL -> default value: 1m
```

### Workers, subscriptions and the janitor

- Every signal subscription row belongs to one worker (`worker_id`, required) and carries an `expires_at`
  (`PYCK_WORKFLOW_SUBSCRIPTION_TTL` after the last registration). Workers refresh it with a heartbeat.
- `registerWorkflow` requires `workerID`. Workflow names are unique per tenant and `task_queue` is
  immutable: registering an existing name under another queue fails with "workflow task queue cannot be changed"
  (to move a workflow: stop the old workers, delete the workflow, deploy on the new queue).
- `unregisterWorker(workerID)` marks the caller's live subscriptions stopped (`stopped_at`) in one update and
  returns how many (`stopped`). It deletes nothing and never touches workflow rows. A stop is a hint; the
  decision is made per event by the router (`services.PreferRunning`): per workflow, if any live subscription
  is not stopped only those route, otherwise the stopped ones keep routing until their TTL. So a stopped old
  version stops routing as soon as a running worker holds subscriptions on the workflow (rolling deploy), a
  crashed worker's rows keep routing until their TTL, and a clean stop of the last worker bridges the gap
  until its replacement registers. A crash and a clean stop of the last worker both end at the TTL.
  `registerWorkflow` (including a heartbeat) clears `stopped_at` on the worker's own rows.
- The janitor (every `PYCK_WORKFLOW_SUBSCRIPTION_JANITOR_INTERVAL`) hard-deletes expired subscriptions and nothing
  else. It never deletes workflow rows: a workflow whose workers are all down keeps its row, and only
  `deleteWorkflow` or tenant teardown removes it. It does not talk to Temporal (Temporal's poller history is in
  memory with a short TTL, so "no pollers" also means an outage or a Temporal restart).
- `unregisterWorker` is a single idempotent update with no read, so it needs no particular isolation level.

### Subscription kinds

A subscription (`temporalSignalType` on `registerWorkflow`) picks what the router does for a matching event.
Kinds and names are part of the subscription's identity: the same topic with another kind or signal name is another
subscription. The type is stored as a string (`temporal_signal_type`), so adding a kind needs no SQL migration.

| Kind | `temporalSignalType` | Signal name | Target | May start |
|---|---|---|---|---|
| Start | `start` | none | `<workflow>_<entity ID>` | yes |
| Broadcast | `intermediate` | required | every running execution of the workflow | no |
| Signal-With-Start | `signal_with_start` | required | `<workflow>_<entity ID>` | yes |
| Signal by ID | `signal_by_id` | required | `<workflow>_<entity ID>` | no |

Per event and workflow the router starts at most once and sends each distinct signal name at most once per target:

- **Signal-With-Start** calls Temporal SignalWithStart on `<workflow>_<entity ID>` with conflict policy `USE_EXISTING`
  (Temporal rejects `FAIL` for it). The event's data is the start input (task queue and search attributes, including
  `pyck_transaction_id`, as for a start) and also the signal, every time, the one that starts the workflow included,
  so a new workflow sees its first event twice, with the same event ID. Workflows dedup by event ID + signal name.
  It carries the start, so a matching plain Start subscription adds nothing.
- **Signal by ID** signals the current run of `<workflow>_<entity ID>` and never starts. With no running execution the
  event is a policy drop: acked and recorded as `dropped` ("no running execution to signal").
- **Broadcast** (`intermediate`) is unchanged, except that it skips the event's own workflow when a Signal-With-Start or
  signal-by-ID subscription already sends that signal name to it.
- **Finished executions.** Signal-With-Start passes the operation's reuse policy (create: only after a failure;
  update: always; delete: never) and Temporal applies it to a finished run. A refusal is a policy drop, recorded as
  `dropped`. The SDK does not say whether the call started the run, so the target is recorded as `signalled`, with
  the run it reached. Request IDs (kind `signal-with-start` / `signal-by-id`) make a redelivery a no-op while the run
  is still running. Once it has closed, Temporal applies the reuse policy without checking request IDs (a repeated
  call would start a second run under update, and be recorded as `dropped` under create and delete). So the router
  stores the event ID in the memo of the run it starts (`pyck_event_id`), and on a redelivery (delivery count above
  1) it describes the latest run first: if that run is closed and carries this event's ID, it sends nothing and records
  the target as `signalled`. In every other case, a failed describe included, it sends the call as usual. Remaining
  limit: if the first delivery signalled a run started by a different event, and that run closed before the
  redelivery, the redelivery still follows the reuse policy (Temporal does not expose which signal request IDs a
  closed run received).

### Signal router

`services.SignalRouter` turns events into Temporal starts and signals. It reads them through one durable JetStream
pull consumer (`workflow-signal-router`, `services/signalrouter_consumer.go`) on the event stream, shared by all
replicas: each event goes to one replica, is handled at least once, and is acknowledged only when every target is done.

| What | Value |
|---|---|
| Filter subjects | `<stream>.*.crud.*.*.*.*` (mutation events), `<stream>.*.temporal.*.*.*.*.*` (workflow state changes). Custom, workflow, attribute-change, dead-letter and `request.reply.*` subjects are not delivered. |
| Deliver policy | `New`, applied when the consumer is created. An existing consumer keeps its position. |
| Ack policy | Explicit. The router sends `InProgress` every `ACK_WAIT/3` while a handler runs. |
| Concurrency | `PYCK_WORKFLOW_ROUTER_CONCURRENCY` handlers per replica; `MAX_ACK_PENDING` events in flight overall. |

Outcomes of one event:

| Outcome | Result |
|---|---|
| Every target done, or no subscription matches, or namespace is not a tenant | ack |
| Start refused because the workflow ID exists; signal to an execution that has closed | ack (success) |
| Malformed event, unknown operation or signal type, bad topic or filter rule | ack, logged (permanent) |
| Temporal `Unavailable`/`DeadlineExceeded` (or the same gRPC codes), deadline, network or DB connection error, and a probe fails | pause: the event is held (`InProgress`) and handled again on the same delivery when the probes pass; no attempt is used |
| Temporal `ResourceExhausted` (rate limit), or `Unavailable`/`DeadlineExceeded` while every probe passes (Temporal's own database or history is down) | held per event, no pause: retried after `RESUME_MIN_INTERVAL` backing off to `RESUME_MAX_INTERVAL`, for at most 15 minutes (not configurable); after that an ordinary failed delivery |
| Any other error, including a network or DB error while every probe passes | NAK with delay from `NAK_BACKOFF` by delivery number (the last entry repeats) |
| Still failing on delivery `MAX_DELIVER` | ack, error log of the failed targets, `workflow_signal_router_events_given_up_total` |

Pausing: the router probes the database (`SELECT 1` on the health pool) and Temporal (gRPC health check on the root
client) every `HEALTH_INTERVAL`, each with `HEALTH_TIMEOUT`. While a probe fails it fetches nothing, probes every
`RESUME_MIN_INTERVAL` backing off to `RESUME_MAX_INTERVAL`, and sets `workflow_signal_router_paused` to 1; events
wait in the stream (3 days of `MaxAge`). While it stays paused it logs a warning every
`PAUSED_WARN_INTERVAL` ("still paused", with the failing probe). The router is not part of `/health/ready`: the
pause is its own protection, and a Temporal outage must not take the whole service out of rotation.

Holding instead of NAKing is deliberate: a NAK counts as a delivery
(`NumDelivered+1`) and the router gives up on an event after `MAX_DELIVER` deliveries (the consumer has no
server-side limit, which would drop an event silently), so NAKing through an outage would burn attempts. A replica that dies while holding frees the event after `ACK_WAIT`.

`Stop()` stops pulling, waits for in-flight handlers to settle their events (events held by a pause are released),
then closes the Temporal clients.

### Signal router and deduplication

The signal router (`services/signalrouter.go`) turns mutation events and Temporal state changes into
workflow starts and signals. It keeps no record of what it delivered; Temporal deduplicates for it.

- **Event ID.** Each mutation event carries `event_id`, the ID of the outbox row that published it (stable across
  publish retries; `id` stays the entity ID). A state change has no outbox row, so its event ID is a UUIDv5 of
  (namespace, workflow ID, run ID, status). The router sends the event ID to Temporal in the `pyck-event-id`
  header on every start and signal; `workflowsdk.EventID` and `workflowsdk.ReceiveEvent` return it to the workflow.
- **Request ID.** Every start and signal also gets a deterministic Temporal request ID: a UUIDv5 of
  (event ID, workflow name, kind `start` or `signal`, signal name, target). The target is the workflow ID for a
  start, and the workflow ID plus run ID of each running execution for a signal, so a broadcast signal gives every
  target its own ID. `common/services/temporal.RequestIDInterceptor`, on the root Temporal connection, writes it
  onto the request.
- **Effect.** A redelivered event, two replicas handling the same event, or a crash between a Temporal call and the
  acknowledgement repeat calls with request IDs Temporal has already seen, and Temporal drops them: a repeated
  signal is ignored, and a repeated start returns the run the first one created instead of an error or a second run.
- **One run per entity per workflow.** Start subscriptions use the workflow ID `<workflow>_<entity ID>`. A start with a
  *different* request ID for a workflow ID that is running is refused with `WorkflowExecutionAlreadyStarted` under
  all three policies (create, update, delete), and recorded as `already_running`. The event's data is not delivered
  to the running execution: to receive every update, subscribe with Signal-With-Start (or signal by ID). Earlier
  versions handed back the running run for update and delete events and recorded a start that never happened.
- **No event ID, no deduplication.** An event from a publisher that predates `event_id` has a nil ID. The router
  does not invent one: its calls go out with the SDK's random request IDs and no header (logged at debug level).
- **Limits.**
  - A start's request ID is only remembered while its run is the workflow ID's current run. `update` events of one
    entity share a workflow ID, so an `update` redelivered after a newer run has replaced its own starts again.
    It is rare; workflows can drop it by event ID (see the workflowsdk README).
  - A signal to an execution that has closed is skipped, not an error: the router only signals running
    executions, so a redelivery after the execution closed signals nothing.
  - Signal request IDs stay in an execution's state until it closes. Temporal caps an execution at 10,000
    signals; each ID adds 36 bytes.

### Routing status

When the router settles a mutation event, it records what it did in a NATS key-value bucket, so a client
can tell "not routed yet" from "routed, nothing started". The bucket is best-effort: a failed write is
logged and the event is acknowledged anyway.

| | |
|---|---|
| Bucket | `<stream>_routing_status` (`pyck_routing_status`), created on first write with the event stream's replica count |
| Expiry | 72h (the stream's `MaxAge`); no janitor |
| Key | `<tenant ID>.<transaction ID>.<event ID>` |
| Written | when the event is settled: handled, failed for good, or given up on after `MaxDeliver` deliveries. Not on a NAK. |
| Not written | events without a transaction ID (`pyck_transaction_id` search attribute) or without an event ID, and state changes |

The value is JSON (`services.RoutingStatus`):

```json
{
  "tenant_id": "…", "transaction_id": "…", "event_id": "…",
  "outcome": "done",
  "sequence": 42,
  "targets": [
    {"kind": "started",   "workflow": "order", "workflow_id": "order_…", "run_id": "…"},
    {"kind": "signalled", "workflow": "order", "workflow_id": "order_…", "run_id": "…", "signal": "Approved"}
  ],
  "recorded_at": "2026-09-30T12:00:00Z"
}
```

- `outcome`: `done` (finished: delivered, or refused for good, such as a malformed subscription) or `gave_up`
  (retries exhausted; the `failed` targets say what is left undone).
- `sequence`: the event's sequence in the stream.
- Target `kind`:

  | kind | meaning |
  |---|---|
  | `started` | a workflow was started, or the start returned the run an earlier delivery started (`workflow_id`, `run_id`) |
  | `already_running` | the start was refused because the workflow ID is taken by another event's run (a running one, or a finished one the reuse policy does not replace) |
  | `signalled` | a running execution was signalled (`workflow_id`, `run_id`, `signal`) |
  | `dropped` | nothing was delivered, for the `reason` given: no running execution, the execution closed first, or a malformed subscription |
  | `failed` | the delivery failed with `error` |

- An event that matched nothing has `"targets": []`: finished, with nothing to do.
- Only the last delivery's targets are recorded. A redelivered start or signal is deduplicated by its request ID and
  reports the same result, so earlier successes show up again.

#### Reading it: `transactionRouting`

```graphql
query {
  transactionRouting(transactionID: "…") {
    entries { eventID outcome sequence targets { kind workflowID runID signal reason error } }
  }
}
```

It returns the transaction's entries for the caller's tenants, oldest first (by `sequence`). The lookup builds its key
filter from the request's own tenant IDs (`<tenant>.<transaction ID>.*`) and drops any entry whose stored tenant or
transaction disagrees with the key, so a caller never reads another tenant's entries. A tenant the caller has no role in
is refused by the tenant middleware before the resolver runs. Nothing recorded yet, or no bucket yet, is an empty list,
not an error.

**Is routing complete?** Every mutation result carries `eventCount`, the number of events the transaction published.
All root mutation fields of one operation share one transaction and one counter, so each field's `eventCount` is the
running total when that field resolves and the last field holds the total. Compare it with the number of entries:

| `eventCount` | entries | meaning |
|---|---|---|
| `0` | any | nothing was published, nothing to route |
| `n` | fewer than `n` | not routed yet (or an event has no status: see below); ask again, with a deadline |
| `n` | `n` | routing is complete: each entry says what happened, and `workflowExecutions(where: {transactionID})` lists the executions the transaction started |

Events without a `pyck_transaction_id` or an `event_id` (from a publisher that predates them) are routed but never get an
entry, so a transaction that includes one stays below its `eventCount`. A custom event sent by `sendCustomEvent` does
carry both. Status is best effort: a status write can fail and events can be skipped, so an entry can be missing for good.
Poll with a deadline, like `workflowExecutions`. Entries expire after 72 hours.
