# Graceful shutdown

Worst-case wall-clock time from SIGTERM to process exit, per service: which
steps consume it, which configuration knobs move it, and how to size the
container stop grace period (`stop_grace_period` in compose,
`terminationGracePeriodSeconds` in Kubernetes).

Applies to the seven GraphQL services wired through
`backend/common/http.Server` (main-data, inventory, management, picking,
receiving, file, workflow), which share the same mechanism, defaults, and
therefore the same budget unless a service overrides a knob.

## Shutdown ladder

On SIGTERM the service walks this sequence. Steps marked **bounded** carry
their own deadline; steps marked **unbounded** have no deadline of their own
and rely on the container stop grace period (SIGKILL) as the watchdog.

| # | Step | Bound | Source of the bound |
|---|------|-------|---------------------|
| 1 | Readiness flips to 503, server keeps serving (pre-drain) | **always exactly** `PYCK_HTTP_SHUTDOWN_PREDRAIN_DELAY` (3s) | fixed sleep in `common/http/serve.go` — paid in full on every shutdown, even when idle |
| 2 | Listener closes, in-flight HTTP requests drain | ≤ `PYCK_HTTP_SHUTDOWN_DRAIN_TIMEOUT` (10s) | `http.Server.Shutdown` deadline; returns early when idle. Requests still running at the deadline are abandoned and their contexts cancelled |
| 3 | Idempotency janitor stops | ~0 | cancel-only; at worst one in-flight prune query errors softly |
| 4 | Temporal worker stops | ≤ Temporal SDK default RPC timeout | `AggregatedWorker.Stop()` calls `shutdownWorker()`, a blocking `ShutdownWorker` gRPC call bounded by the SDK's default RPC timeout; failure only logs a warning. `worker.Options.WorkerStopTimeout` is unset (0) → pollers stop but in-flight tasks are **not** awaited; abandoned tasks time out server-side and are retried per their activity retry policy |
| 5 | Temporal client closes | ~0 | closes the gRPC connection |
| 6 | Data-types cache listener stops | ~0 | cancel-only; its NATS consumer drain completes under step 8's connection drain |
| 7 | Event system (outbox) stops | **no own deadline** — see below | `OutboxHandler.Stop()` waits for an in-progress publish batch to finish |
| 8 | NATS connection drains | ≤ 5s | `natsDrainTimeout` constant in the service `main.go`; hard `Close()` on expiry |
| 9 | DB pools close | ~0 | `sql.DB.Close` does not wait for busy conns; stragglers were already cancelled in step 2 |
| 10 | Tracer flushes | ≤ `PYCK_OTEL_SHUTDOWN_TIMEOUT` (5s) | OTLP force-flush; pays the **full** 5s whenever the collector is unreachable |

## The one unbounded step: outbox stop

`OutboxHandler.Stop()` closes the stop channel and waits for the three
handler goroutines. A goroutine idle in its `select` exits immediately; the
worst case is a `processOutbox` batch that started just before the signal,
which runs to completion:

- **Fetch / persist phases** are DB transactions with no context deadline —
  DB-bound, normally milliseconds.
- **Publish phase** is sequential over up to `PYCK_OUTBOX_BATCH_SIZE` (100)
  transaction *groups* (a group can hold several entries). Every entry is a
  JetStream publish that waits for its ack, 5s (nats.go `defaultAPITimeout`,
  not configurable).
  The first failure in a group skips the group's remaining entries, so the
  failure-mode ceiling is ≈ `BATCH_SIZE × 5s = 500s`: for example a full
  backlog batch while JetStream does not ack, each publish timing out in
  turn. The DLQ drain goroutine has the same shape and the same ceiling.

500s is far beyond any sane grace period, deliberately: the outbox is
at-least-once. Rows not marked published survive in the table, the claim
lease (`PYCK_OUTBOX_CLAIM_LEASE`, 30s) expires, and a surviving replica's
poll or the next startup republishes them; JetStream message-ID dedup
suppresses duplicates of anything that was published but not yet marked. So
when a busy outbox meets SIGKILL, nothing is lost — shutdown is merely
non-graceful. Bounding this step with its own deadline would add no
guarantee the persisted row does not already provide.

## Worst-case sums (defaults)

- **Deterministic worst case** (every bounded deadline fully spent, outbox
  idle): 3 + 10 + 5 + 5 = **23s**, leaving 7s of the 30s grace period as
  slack for the DB-bound teardown work.
- **Observed happy path** (idle service, collector reachable): ~3.5s;
  ~8.3s with the tracer flush timing out against an absent collector.
- **Pathological case** (outbox mid-batch against dead responders): exceeds
  the grace period; the orchestrator's SIGKILL ends it, safely (see above).

## Sizing rule

```
stop_grace_period ≥ PREDRAIN_DELAY + DRAIN_TIMEOUT
                  + natsDrainTimeout + OTEL_SHUTDOWN_TIMEOUT
                  + slack (≥5s, DB-bound teardown)
```

With defaults: 3 + 10 + 5 + 5 + 5 → the compose `stop_grace_period: 30s`
fits exactly. Raise the grace period whenever any knob below is raised;
Kubernetes deployments need `terminationGracePeriodSeconds ≥ 30`.

## Startup abort

A stop signal received during startup aborts at the next checkpoint rather
than completing startup and briefly serving traffic. Checkpoints sit between
startup phases: after the management dependency check, after migrations,
after NATS setup, after service-specific components, and — the mandatory
one — immediately before the HTTP server starts.

Migrations and (in `management`) the bootstrap phase are deliberately **not**
cancellable: a half-applied migration is worse than a slow shutdown, and
`golang-migrate` holds an advisory lock whose interruption can block future
deploys.

An abort exits **0** and logs at info (`startup aborted`, naming the step it
stopped after) — a normal stop request, not a failure. All registered
teardown still runs: the abort returns an error up through `run()`, so every
`defer` fires. The pre-drain window (step 1 above) is skipped, because no
listener was ever bound and no load balancer is routing to the instance.

| Service | Checkpoints |
|---------|-------------|
| file | 5 |
| workflow | 5 |
| inventory | 5 |
| main-data | 4 |
| picking | 4 |
| receiving | 4 |
| management | 6 |

An abort is not guaranteed to be fast: the outbox teardown still runs its
in-progress poll to completion, the same unbounded step described above for
normal shutdown.

Implementation lives in `backend/common/startup`.

## Probe model

`/health` and `/health/ready` answer different questions and are wired to
different Kubernetes probes:

| Path | Probe | Checks | During 3s pre-drain | During drain |
|---|---|---|---|---|
| `/health` | liveness + startup | nothing — static `200` | `200` | `200` |
| `/health/ready` | readiness | draining flag, then a DB `SELECT 1` bounded at 2s | `503` | `503` |

- **Liveness checks nothing, deliberately.** It answers "can this process
  still run a handler". A liveness probe that consults Postgres restarts
  every replica of every service during a database blip, converting a
  recoverable outage into a restart storm. Dependency health belongs on
  readiness, which sheds traffic without restarting.
- **A draining instance stays live on purpose** — restarting a pod mid-drain
  discards its in-flight requests.
- **Readiness flips to 503 the instant SIGTERM lands**, before the pre-drain
  sleep begins (step 1 above), so load balancers stop routing while the
  listener is still accepting. That ordering is what makes the pre-drain
  window useful.
- **`startupProbe` targets `/health`, not readiness.** The HTTP listener
  binds as the last statement of startup, after the management dependency
  wait and migrations, so "answered on the port" is already an exact
  startup gate. Pointing startup at readiness would restart-loop on a
  database outage.
- **No `preStop` hook, deliberately.** Kubernetes runs `preStop` *before*
  delivering SIGTERM and starts the grace-period clock at `preStop`. A
  `preStop: sleep` would therefore keep readiness answering 200 for its
  whole duration (the in-process flip is SIGTERM-triggered), duplicate the
  pre-drain the process already performs, and bill both against the same
  budget. The in-process design flips readiness first and then waits,
  which is the correct order.
- **`terminationGracePeriodSeconds: 45`** — the deterministic worst case is
  ~23s plus headroom for the one unbounded step (the outbox batch). Note
  that this is currently unset in the deployment manifests, so pods run on
  the Kubernetes default of 30s.

Recommended probe values:

| Probe | `periodSeconds` | `timeoutSeconds` | `failureThreshold` |
|---|---|---|---|
| startup | 5 | — | 60 (5 min) |
| liveness | 10 | 2 | 3 |
| readiness | 5 | 3 | 2 |

**Caveat:** the 2s readiness check bound is a placeholder chosen without
latency data and needs review against p99 `SELECT 1` on the dedicated
health pool.

## Configuration knobs

| Knob | Default | Contribution to the total |
|------|---------|---------------------------|
| `PYCK_HTTP_SHUTDOWN_PREDRAIN_DELAY` | 3s | always paid in full |
| `PYCK_HTTP_SHUTDOWN_DRAIN_TIMEOUT` | 10s | cap; early-exit when idle |
| `natsDrainTimeout` (const, service `main.go`) | 5s | cap on the NATS drain |
| `PYCK_OTEL_SHUTDOWN_TIMEOUT` | 5s | cap; paid in full when the collector is down |
| `PYCK_OUTBOX_BATCH_SIZE` × JetStream publish ack timeout | 100 × 5s | ceiling of an in-progress outbox batch (unbounded step; SIGKILL is the watchdog) |
| `PYCK_OUTBOX_CLAIM_LEASE` | 30s | not part of the sum — recovery latency for rows a killed batch had claimed |
| `PYCK_OUTBOX_PRUNE_INTERVAL` / `_RETENTION` / `_BATCH_SIZE` | 5m / 72h / 1000 | not part of the sum: the published-row janitor stops on context cancel and an interrupted sweep just resumes next start |
| JetStream publish ack timeout (nats.go) | 5s | per entry in the outbox batch; not configurable |
| `WorkerStopTimeout` (Temporal, unset) | 0 | worker stop does not wait; raising it adds directly to the sum |
| `stop_grace_period` / `terminationGracePeriodSeconds` | 30s | the SIGKILL watchdog bounding everything above |

Knobs that look shutdown-relevant but are not: `PYCK_TX_RETRIES` (50) —
serializable-retry loops of in-flight requests cannot extend the drain,
because step 2 abandons and cancels them at its deadline.

## Per-service worst case

All seven GraphQL services share the defaults above, so their budgets are
identical until a service overrides a knob. Service-specific extras that
ride the shutdown path but add no bounded time of their own: inventory's
stock service close (~0), workflow's signal router stop (waits for its
consumers like the outbox stop does) and subscription janitor (cancel-only),
and management's Zitadel gRPC connection close (~0). Management's Temporal
worker stop is not one of these — like the other services' Temporal
workers, it is bounded by the Temporal SDK's default RPC timeout (step 4
above), not by `WorkerStopTimeout`, which is unset.

| Service | Deterministic worst case | Grace period | Verified happy path |
|---------|--------------------------|--------------|---------------------|
| file | 23s | 30s (compose) | 8.3s (`docker stop`, exit 0) |
| main-data | 23s | 30s (compose) | — |
| inventory | 23s | 30s (compose) | — |
| management | 23s | 30s (compose) | — |
| picking | 23s | 30s (compose) | — |
| receiving | 23s | 30s (compose) | — |
| workflow | 23s | 30s (compose) | — |
