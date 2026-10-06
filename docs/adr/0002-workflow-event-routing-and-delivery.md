# ADR-0002: Workflow event routing and delivery

## Status

Accepted (v0.25.3)

## Context

A committed mutation is meant to start or signal every workflow that
subscribes to its event, however many workers, replicas and versions run.
Up to v0.25.2 it did not:

| Problem | Effect |
|---|---|
| A worker deleted every workflow in its tenant that was not in its own registry, on every start | Two workers with different workflows deleted each other's subscriptions (#1564) |
| The subscription janitor deleted workflows when Temporal listed no pollers | Workflows vanished after a worker outage of about 5 minutes or a Temporal restart, because Temporal keeps poller history in memory with a short TTL |
| All Temporal workers of a process shared one worker ID | One task queue's stop dropped the other queues' subscriptions |
| Subscriptions expired 15 minutes after the last heartbeat | Events in a longer worker outage were dropped, though Temporal would have queued them |
| The router used only the first matching subscription per workflow | Two workers or versions subscribing one workflow with different signals or filters: only one was served |
| The router consumed events through core-NATS `QueueSubscribe` | At most once: a router failure lost the trigger (#1358) |
| State-change starts used a random workflow ID | A repeated state change started a second execution |
| The Temporal-side publisher dropped a state change after a 100ms timeout | A short NATS hiccup lost follow-up starts |
| Mutations waited for a workflow reply while marshaling the response | Unreliable, and blocked caching the response for idempotency (#1298, #1123) |
| Published outbox rows were never deleted | Every service's outbox table grew without bound |

## Decision

### Subscriptions belong to the worker that registers them

- Each Temporal worker registers the subscriptions of exactly the workflows on
  its task queue, under its own worker ID `<client identity>/<task queue>`.
  A worker never lists or deletes other workers' workflows.
- Subscriptions expire 1 hour after the last heartbeat; the heartbeat runs
  every 5 minutes.
- On stop the SDK calls `unregisterWorker` per worker ID, which marks that
  worker's live subscriptions stopped (`stopped_at`) in one update and deletes
  nothing. The router decides per event: while any worker holds running
  (unstopped) subscriptions on a workflow only those route; once all are
  stopped they keep routing until their TTL lapses, so events keep starting
  executions that Temporal queues until the workers return. A crashed worker is
  never marked, so its rows route until the TTL as well. A later registration
  (the heartbeat) clears the mark.
- The janitor deletes expired subscriptions only. It never deletes workflow
  rows and does not talk to Temporal.
- pyck does not track worker versions: Temporal picks the version for a start
  and delivers each signal to its execution's version.

Rules for workflow developers: workflow names are unique per tenant across all
workers; one name maps to one task queue (move it with `deleteWorkflow`, then
deploy); workflows tolerate signals they do not handle.

### Every matching subscription is routed

- Per event the router matches every live subscription in the tenant, each by
  its own topic and FEEL filter. A workflow starts at most once per event, and
  each distinct signal name goes once to each target.
- Kinds: start, broadcast, Signal-With-Start and signal by ID. The workflow ID
  is always `<workflow>_<entity ID>` (the event's `id`, not its `event_id`); no
  computed IDs.
- A start refused because its workflow ID is taken is a success, recorded as
  `already_running`. A worker's own start options (conflict or reuse policy)
  are not used by the router; the operation's policy applies.
- State-change starts use a stable ID derived from (namespace, workflow ID,
  run ID, status), and filter on the state-change message.

### Delivery is at least once, deduplicated by Temporal

- One durable JetStream pull consumer, shared by all workflow-service
  replicas, replaces the `QueueSubscribe`s. It is created with
  `DeliverPolicy: New`, acknowledges an event only when every target is done,
  NAKs transient failures with backoff, and gives up after `MaxDeliver`.
- While Temporal or the database is unreachable the router holds its events
  instead of NAKing them, so an outage does not use up delivery attempts.
- Every event carries an event ID (the outbox entry ID, stable across
  retries). The router passes it to the workflow in the `pyck-event-id` header.
- The router keeps no record table. It sets every Temporal request ID
  deterministically from (event ID, workflow, kind, signal name, target)
  through a gRPC interceptor on the root Temporal connection; Temporal drops
  repeated signals and returns the existing run for a repeated start.
- The Temporal-side publisher retries failed state-change publishes and sets a
  JetStream message ID (`statechange-` plus the UUIDv5 of namespace, workflow
  ID, run ID and status), so the copies that several Temporal pods publish
  collapse into one within the stream's duplicate window. The ID is
  predictable: a client that can publish to the stream could suppress a real
  state change within that window by guessing it. That risk is accepted until
  each tenant has its own stream (#1656); no HMAC or secret key is used. A
  change stored twice beyond the window, or by a retry, is absorbed by the
  Temporal request IDs above.

### Mutations return a handle

- Mutation results lose `workflows` and gain `transactionID` and `eventCount`.
  No reply wait, no post-commit patching.
- `transactionRouting(transactionID)` reads the routing status the router
  writes per settled event to a NATS KV bucket (72h expiry, best effort). A
  client compares the entry count with `eventCount` to tell "not routed yet"
  from "routed, nothing started".
- `pyck_transaction_id` is a Keyword search attribute, the tenth. Temporal's
  SQL visibility (postgres12) has exactly Keyword01..10 per namespace, so all
  Keyword slots are used: no further Keyword attribute can be added, and an
  operator must not register a custom Keyword attribute in pyck namespaces.

### Outbox retention

Each service prunes published outbox rows older than 72h in batches, using a
partial index on `published_at`. Publish retries go from 10 to 15, so the
outbox retries for about 4 hours before it dead-letters an event.

Details, settings and defaults live in the code's documentation:
`backend/workflow/README.md` (subscriptions, router, deduplication, routing
status) and `backend/workflowsdk/README.md` (worker IDs, subscription kinds,
event IDs).

## Alternatives rejected

| Alternative | Why not |
|---|---|
| Versioned subscriptions: rules per deployment version, removals before promotion, refusing old SDKs | Review found outage scenarios that lose subscriptions; Temporal already routes by version |
| Janitor deletes workflows nobody polls | Poller history is in memory with a short TTL, so an outage looks like an abandoned workflow |
| A router-side record table with claims and leases | Temporal already deduplicates starts and signals by request ID |
| Setting request IDs through the SDK | The Go SDK sets random request IDs and has no public setter (checked in v1.42.0 and v1.49.0) |
| NAKing during an outage | A NAK counts as a delivery, so an outage would exhaust `MaxDeliver` and drop healthy events |
| Deprecating `workflows` first | The handle needs no reply wait at all; a deprecation period would keep the wait alive |

## Consequences

### Positive

- Workers with different workflows, task queues or versions no longer break
  each other's routing.
- A committed mutation's events reach every subscriber at least once,
  through router crashes, redeliveries and Temporal or database outages.
- Workflows can deduplicate by event ID plus signal name.
- Mutation responses are final when the transaction commits, so #1123 can
  cache them.
- Outbox tables stay bounded.

### Negative

- **Breaking for clients.** Every client selecting `workflows` on a mutation
  result fails validation; generated clients of v0.25.2 and v0.25.3 are
  mutually incompatible. v0.25.3 therefore rolls out in one drained window:
  gateway down, workers stopped, outboxes and router drained, every service and
  worker deployed at once.
- **Delivery can repeat.** Temporal drops most repeats, but not an `update`
  redelivered after a newer run replaced its own, nor a Signal-With-Start
  redelivered after its run closed: Temporal applies the reuse policy there
  without checking request IDs. The router closes the common case: a run
  started by Signal-With-Start records its event ID in its memo, and a
  redelivery that finds the latest run closed with its own event ID sends
  nothing. What remains is a Signal-With-Start that signalled a run started
  by a different event, which closed before the redelivery: `update` then
  starts a second run and `create` and `delete` record it as dropped.
  Workflows that must not apply an event twice keep the keys they handled.
- **No ordering** across replicas: two events of one entity can be handled out
  of order.
- **A start does not deliver an update.** A start that finds its workflow ID
  running is recorded as `already_running`; the running execution does not see
  the event. Workflows that need every update use Signal-With-Start.
- **Lost history after a long outage.** The stream deletes the consumer after
  72h without a pull; it is then recreated at `DeliverPolicy: New` and that
  outage's events are skipped.
- **Repeated state changes.** A run paused and resumed enters RUNNING twice
  with the same (namespace, workflow ID, run ID, status). The router derives
  the same Temporal request ID for both, so within Temporal's request-ID
  retention the second one starts nothing new.
- **Events with no subscriber** are acknowledged and only logged at debug
  level.
