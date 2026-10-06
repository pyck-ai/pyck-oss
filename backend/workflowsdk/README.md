# workflowsdk

A Go SDK for building [Temporal](https://temporal.io) workflows in the Pyck platform. This package provides a streamlined interface for defining, registering, and executing durable workflows with automatic activity registration and signal handling.

## Overview

`workflowsdk` simplifies Temporal workflow development by providing:

- **Type-safe workflow interfaces** with generic input/output types
- **Automatic workflow and activity registration** via a global registry
- **Built-in worker management** with sensible defaults
- **Signal handling** for event-driven workflow control
- **Environment-based configuration** for Temporal connections
- **HTTP client utilities** with authentication support
- **Integration with Pyck's logging and observability stack**

## Core Concepts

### Workflow Interface

Workflows implement the `Workflow[I, O]` interface where `I` is the input type and `O` is the output type:

```go
type Workflow[I, O any] interface {
    Setup(ctx context.Context) (any, error)
    Execute(ctx workflow.Context, input I) (O, error)
}
```

- **Setup**: Called once during worker initialization to create activity structs that can hold shared state
- **Execute**: The main workflow logic, executed by Temporal for each workflow instance (must be deterministic)

### State Management: Critical Distinction

**Workflow Structs MUST NOT Contain State**

The workflow struct (e.g., `MyWorkflow{}`) is instantiated **once** during worker initialization and shared across all workflow executions. Storing state in workflow structs breaks Temporal's replay/determinism guarantees and will cause non-deterministic behavior.

```go
// ❌ WRONG - breaks determinism
type MyWorkflow struct {
    counter int  // This will be shared across ALL workflow executions!
}

// ✅ CORRECT - stateless workflow struct
type MyWorkflow struct{}
```

**Activity Structs CAN and SHOULD Contain State**

Activity structs are created in `Setup()` and can safely hold shared resources like:
- Database connections and connection pools
- HTTP clients with authentication
- Configuration values
- Caches and Redis clients
- Logger instances

Activities are NOT replayed, so they can have side effects and use stateful resources.

```go
// ✅ CORRECT - activity struct with shared state
type MyActivities struct {
    db     *sql.DB
    cache  *redis.Client
    config *Config
    logger *zerolog.Logger
}

func (w *MyWorkflow) Setup(ctx context.Context) (any, error) {
    return &MyActivities{
        db:     database.NewConnection(),
        cache:  redis.NewClient(),
        config: loadConfig(),
        logger: pycklog.ForContext(ctx),
    }, nil
}
```

### Optional Interfaces

Workflows can implement additional interfaces for advanced features:

- **`WorkflowSignals`**: Define signals the workflow can receive
- **`WorkflowData`**: Attach metadata (data ID, type, custom fields)
- **`WorkflowStartOptions`**: Customize task queue and other start options
- **`WorkflowRegisterOptions`**: Configure workflow registration parameters

## Quick Start

### 1. Define Your Workflow

```go
package myworkflow

import (
    "context"
    "time"
    "go.temporal.io/sdk/workflow"
    "github.com/pyck-ai/pyck/backend/workflowsdk"
)

// MyWorkflow struct MUST NOT contain any state.
// It's constructed once during worker initialization and shared across all workflow executions.
// Storing state here breaks Temporal's replay/determinism guarantees.
type MyWorkflow struct{}

type Input struct {
    Value string
}

type Output struct {
    Result string
}

// Setup initializes activities for this workflow.
// This is called once during worker startup and can create stateful activity structs.
func (w *MyWorkflow) Setup(ctx context.Context) (any, error) {
    // Activities CAN and SHOULD contain state like DB connections, HTTP clients, etc.
    // Each activity struct instance is created here and reused across activity executions.
    return &MyActivities{
        // Example: initialize shared resources
        // db:     database.NewConnection(),
        // cache:  redis.NewClient(),
        // config: loadConfig(),
    }, nil
}

// Execute runs the workflow logic.
// This function is called for each workflow execution and MUST be deterministic.
func (w *MyWorkflow) Execute(ctx workflow.Context, input Input) (Output, error) {
    // Configure activity options
    ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
        StartToCloseTimeout: 30 * time.Second,
    })

    // IMPORTANT: Use nil pointer for activity references.
    // Activities are NEVER instantiated in workflow code (that would break determinism).
    // Temporal only needs the type for method resolution - nil prevents accidental use.
    var a *MyActivities
    var result string
    err := workflow.ExecuteActivity(ctx, a.ProcessValue, input.Value).Get(ctx, &result)
    if err != nil {
        return Output{}, err
    }

    return Output{Result: result}, nil
}
```

### 2. Define Activities

```go
// MyActivities holds shared state for activity executions.
// This struct is created once in Setup() and can safely contain:
// - Database connections and connection pools
// - HTTP clients with pre-configured auth
// - Configuration values
// - Caches and connection pools
// - Logger instances
type MyActivities struct {
    // Example stateful fields:
    // db     *sql.DB
    // cache  *redis.Client
    // config *Config
}

func (a *MyActivities) ProcessValue(ctx context.Context, value string) (string, error) {
    // Activity logic here - can safely use a.db, a.cache, etc.
    // Activities are NOT replayed, so they can have side effects.
    return "processed: " + value, nil
}
```

### 3. Register the Workflow

In an `init()` function (typically in the same package):

```go
func init() {
    workflowsdk.MustRegisterWorkflow(&MyWorkflow{})
}
```

### 4. Run the Worker

**Recommended: Use workflowgen for Automatic Import Management**

The easiest way to set up your worker is to use our `workflowgen` code generator, which automatically discovers all workflow packages with `init()` functions and maintains the import list for you:

```go
//go:generate go tool workflowgen ./workflows/...

package main

import (
    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"

    // Workflow imports are auto-generated by workflowgen
    // Run: go generate
)

func main() {
    workflowsdk.RunDefaultWorker()
}
```

Then run `go generate` to automatically scan your `./workflows/...` directory and add all necessary imports.

> **Safe to Customize**: `workflowgen` only updates the underscore import section—your `main()` function and custom code are never overwritten. Feel free to modify the generated file to add custom worker configuration, logging, middleware, etc. See [workflowgen](../workflowgen/README.md) for details.

**Alternative: Manual Import Management**

If you need custom worker configuration or prefer not to use the code generator, you can manually import your workflow packages:

```go
package main

import (
    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"

    // Manually import each workflow package to trigger init()
    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/myworkflow"
    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/anotherworkflow"
)

func main() {
    workflowsdk.RunDefaultWorker()
}
```

> **Note**: With manual imports, you must remember to add a new import line each time you create a workflow. The `workflowgen` approach eliminates this maintenance burden while still allowing full customization of your worker setup.
## Advanced Features

### Signals

Signals allow workflows to receive events while running:

```go
import (
    "context"

    "github.com/google/uuid"
    "github.com/pyck-ai/pyck/backend/common/events"
    "github.com/pyck-ai/pyck/backend/workflowsdk"
)

func (w *MyWorkflow) Signals(ctx context.Context) []*workflowsdk.Signal {
    tenantID := uuid.MustParse("your-tenant-id")

    return []*workflowsdk.Signal{
        workflowsdk.NewStartSignal(
            events.MutationEventTopic{
                TenantID:      tenantID,
                ServiceName:   "data",
                OperationName: "created",
            },
        ),
        workflowsdk.NewIntermediateSignal(
            events.MutationEventTopic{
                TenantID:      tenantID,
                ServiceName:   "approval",
                OperationName: "received",
            }, "approval-received",
        ),
    }
}
```

Signal types (constructor, target, may start):

| Type | Constructor | Target | Starts |
|---|---|---|---|
| Start | `NewStartSignal` | `<workflow>_<entity ID>` | yes |
| Intermediate (broadcast) | `NewIntermediateSignal` | every running execution of the workflow | no |
| Signal-With-Start | `NewSignalWithStartSignal` | `<workflow>_<entity ID>` | yes, with the event as input and as signal |
| Signal by ID | `NewSignalByIDSignal` | `<workflow>_<entity ID>` | no; without a running execution the event is dropped and recorded |

`<entity ID>` is the event's `id`: the entity's ID for data changes. It is not the event's `event_id` (the outbox entry ID).


> **Note on Sparse Topic Structs**:
> - **Omitted fields become wildcards**: Only set the fields you need to match. For example, omitting `SchemaName` will match events from any schema.
> - **Tenant ID is special**: If `TenantID` is not set (or set to zero), it will be automatically replaced with ALL tenant IDs the current user is authorized for. This ensures proper multi-tenant isolation.
> - **Available fields**: `MutationEventTopic` supports `TenantID`, `ServiceName`, `SchemaName`, `EntityID`, and `OperationName`. (Legacy `request.reply.*` registrations are still accepted and normalized to this form.)
> - **Other topic types**: See [backend/common/events](../common/events) for `CustomEventTopic`, `WorkflowEventTopic`, etc.

#### Filter rules and payloads

A `workflowsdk.WithFilterRule("...")` FEEL expression is evaluated against the
event, and the same data is delivered to the workflow (start input or signal
payload). An empty rule matches every event on the topic.

| Topic | Filter variables (top-level JSON fields) | Start input / signal payload |
|-------|------------------------------------------|------------------------------|
| `MutationEventTopic` | the fields of the entity after the change (`DataAfter`) | `DataAfter` |
| `TemporalWorkflowStateChangeTopic` | `namespace`, `task_queue`, `workflow_id`, `workflow_type_name`, `run_id`, `status` | the state-change message, with the same six fields |

For state changes, `status` is the Temporal status the run entered (for example
`RUNNING`, `COMPLETED`, `FAILED`), so `status = "COMPLETED"` matches a finished
run. The payload is **not** the pyck workflow record: read the state-change
fields from the message.

### Event IDs and deduplication

Design background: [ADR-0002](../../docs/adr/0002-workflow-event-routing-and-delivery.md).

Every event the signal router delivers has an event ID (the `event_id` of the
mutation event, stable across redeliveries). The router sends it to Temporal in
the `pyck-event-id` header (`eventid.HeaderKey`) on every start, signal and
signal-with-start. `NewWorker` installs the interceptor that reads it; workflows
need no registration.

| You want | Call | Notes |
|---|---|---|
| The event that started this run | `workflowsdk.EventID(ctx)` | `(uuid.UUID, bool)`; `false` if the run was not started by the router |
| A signal's value and its event | `workflowsdk.ReceiveEvent[T](ctx, ch)` | Use instead of `ch.Receive`; `ev.ID` is `uuid.Nil` (`ev.HasID() == false`) if the sender set none |

The dedup key is **event ID + signal name**. The router can deliver an event
more than once (a crash between the Temporal call and the ack, or two
replicas), and Temporal drops repeats by request ID in most cases, but not all:
a workflow that must not apply an event twice should remember the keys it has
handled. Keep that set in workflow state; it survives replay. Under Signal-With-Start the first event arrives twice, as
the start input and as a signal, both with the same event ID: apply the input, and record its key under the
subscription's signal name so the signal is skipped.

```go
type handled struct {
    ID     uuid.UUID
    Signal string
}

func (w *MyWorkflow) Execute(ctx workflow.Context, in Input) (Output, error) {
    seen := map[handled]struct{}{}

    // The start event's key. Under Signal-With-Start the same event also
    // arrives as a signal with the same event ID, so seed it under that
    // subscription's signal name, not a placeholder: the signal is then
    // recognised as already applied. (A plain Start sends no signal.)
    if id, ok := workflowsdk.EventID(ctx); ok {
        seen[handled{id, "approval-received"}] = struct{}{}
    }

    ch := workflow.GetSignalChannel(ctx, "approval-received")

    for {
        ev, err := workflowsdk.ReceiveEvent[Approval](ctx, ch)
        if err != nil {
            return Output{}, err
        }

        key := handled{ev.ID, "approval-received"}
        if ev.HasID() {
            if _, dup := seen[key]; dup {
                continue // a redelivery of an event already applied
            }

            seen[key] = struct{}{}
        }

        // apply ev.Value ...
    }
}
```

How it works: Temporal only hands a signal's header to an interceptor, so the
worker interceptor copies the event ID into the signal payload's metadata. It
stays attached to its own signal inside the channel, which keeps it correct
when several signals are queued. Ordinary `ch.Receive` still works and ignores
the ID.

### Custom Task Queues

By default, workflows use the `"default"` task queue. Override this:

```go
func (w *MyWorkflow) StartOptions(ctx context.Context) client.StartWorkflowOptions {
    return client.StartWorkflowOptions{
        TaskQueue: "my-custom-queue",
    }
}
```

Each unique task queue gets its own worker instance, allowing workflow isolation.

**One task queue per worker type.** A workflow name is unique per tenant and its
task queue is immutable. Every worker that serves a workflow must register it on
the same queue: registering an existing name under a different queue fails at
startup with `workflow task queue cannot be changed` (the SDK does not retry it).

**Moving a workflow to another task queue:** first stop every worker on the old
queue, then call `deleteWorkflow` for it, then deploy the workers on the new
queue. Both steps are needed. The workflow service never deletes workflow rows
on its own (its janitor only removes expired subscriptions), so the old row
keeps the name on the old queue until `deleteWorkflow` removes it. And a worker
that is still running re-creates the row on the old queue with its next
registration heartbeat, so deleting the workflow while old workers run does not
help.

### Workflow Metadata

Attach metadata for tracking and routing:

```go
func (w *MyWorkflow) Data(ctx context.Context) (uuid.UUID, string, map[string]any) {
    dataID := uuid.New()
    dataType := "my-data-type"
    metadata := map[string]any{
        "tenant_id": "abc123",
        "priority": "high",
    }
    return dataID, dataType, metadata
}
```

### Activity Shared State Example

Here's a complete example showing how to use shared state in activities:

```go
// Activities struct with database and HTTP client
type DataProcessorActivities struct {
    db         *sql.DB
    httpClient *http.Client
    apiToken   string
}

func (w *DataProcessorWorkflow) Setup(ctx context.Context) (any, error) {
    // Initialize shared resources once during worker startup
    db, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
    if err != nil {
        return nil, err
    }

    return &DataProcessorActivities{
        db:         db,
        httpClient: workflowsdk.NewDefaultHTTPClient(workflowsdk.Config.APIClientConfig.Token),
        apiToken:   workflowsdk.Config.APIClientConfig.Token,
    }, nil
}

// Activity can use the shared db connection
func (a *DataProcessorActivities) FetchData(ctx context.Context, id string) (*Data, error) {
    var data Data
    err := a.db.QueryRowContext(ctx, "SELECT * FROM data WHERE id = $1", id).Scan(&data)
    return &data, err
}

// Activity can use the shared HTTP client
func (a *DataProcessorActivities) CallExternalAPI(ctx context.Context, endpoint string) error {
    resp, err := a.httpClient.Get(endpoint)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    // Process response...
    return nil
}
```

## Common Pitfalls

### ❌ Wrong: Using activity instance in workflow Execute()

```go
func (w *MyWorkflow) Execute(ctx workflow.Context, input Input) (Output, error) {
    // WRONG: Instantiating activities in workflow code breaks determinism!
    // The instance is never actually used - only serves as a type reference.
    activities := &MyActivities{}
    err := workflow.ExecuteActivity(ctx, activities.Process, input).Get(ctx, &result)
    // ...
}
```

### ✅ Correct: Using nil pointer for activity reference

```go
func (w *MyWorkflow) Execute(ctx workflow.Context, input Input) (Output, error) {
    // CORRECT: Use nil pointer for type-safe activity reference.
    // Temporal only needs the type - nil makes it obvious if accidentally dereferenced.
    var a *MyActivities
    err := workflow.ExecuteActivity(ctx, a.Process, input).Get(ctx, &result)
    // ...
}
```

### ❌ Wrong: Storing state in workflow struct

```go
type MyWorkflow struct {
    counter int  // WRONG: Shared across all executions!
}

func (w *MyWorkflow) Execute(ctx workflow.Context, input Input) (Output, error) {
    w.counter++  // Non-deterministic!
    // ...
}
```

### ✅ Correct: Workflow state only in Execute() scope

```go
type MyWorkflow struct{}  // CORRECT: Stateless

func (w *MyWorkflow) Execute(ctx workflow.Context, input Input) (Output, error) {
    counter := 0  // Local variable - safe and deterministic
    counter++
    // ...
}
```

## Best Practices

1. **Keep workflows deterministic**: Avoid random values, current time, or external calls in workflow code
2. **Never store state in workflow structs**: Workflow structs are shared across executions and break replay
3. **Use activity structs for shared state**: Database connections, HTTP clients, config belong in activity structs
4. **Use activities for side effects**: Database calls, HTTP requests, file I/O should be in activities
5. **Always use nil pointers for activities**: Use `var a *MyActivities` - never instantiate (`&MyActivities{}`) in workflow code. Nil pointers provide type safety while preventing accidental dereferencing that would break determinism.
6. **Handle errors gracefully**: Return errors from Execute() to trigger retries
7. **Set appropriate timeouts**: Configure activity timeouts based on expected duration
8. **Use signals for external events**: Allow workflows to react to external state changes
9. **Version workflows carefully**: Temporal requires workflows to be backward compatible
10. **Test workflows**: Use Temporal's testing framework to validate workflow logic

## Configuration

The SDK loads configuration from environment variables via `LoadEnv()`:

### Required Variables

- **`PYCK_API_TOKEN`**: Authentication token for Pyck API. Its user needs the
  WRITER role on the tenant: `registerWorkflow` writes subscription rows and
  `unregisterWorker` marks them stopped, and both refuse a READER (`writer role
  required`). Service users are granted WRITER when they are created.
- **`PYCK_API_TENANT_ID`**: Tenant UUID

### Temporal Configuration

- **`TEMPORAL_HOST_URL`**: Temporal server address (default: `localhost:7233`)
- **`TEMPORAL_NAMESPACE`**: Temporal namespace (default: `default`)
- **`TEMPORAL_CLIENT_KEY_PATH`**: Path to mTLS client key (optional)
- **`TEMPORAL_CLIENT_CERT_PATH`**: Path to mTLS client cert (optional)

See [temporalenvconfig](https://pkg.go.dev/go.temporal.io/sdk/contrib/envconfig) for all supported variables.

### Logging

- **`PYCK_LOG_LEVEL`**: Log level (`debug`, `info`, `warn`, `error`)
- **`PYCK_LOG_FORMAT`**: Log format (`json`, `pretty`)

### Gateway

- **`PYCK_GATEWAY_URL`**: Pyck gateway base URL

### Worker identity and registration

Each worker instance has a unique identity, `<pid>@<host>#<6 random [a-z0-9]>`
(for example `1@pod-7d9f#k3x9qa`), generated once per `NewWorker`. An identity already set in the Temporal client options loaded from the
environment (`temporalenvconfig`) wins. The same string is the
Temporal client identity and the identity the health probe matches pollers
against.

The `workerID` a worker registers its signal subscriptions under is per Temporal
worker (one per task queue): `<identity>/<task queue>`, for example
`1@pod-7d9f#k3x9qa/orders`. A worker registers the subscriptions of exactly the
workflows on that task queue under that ID, refreshes them under it, and calls
`unregisterWorker` once per ID on stop. Two task queues in one process therefore
never share subscription rows.

If you pin the identity yourself (Temporal client options from the environment
or `WithClientOptions`), replicas with the same configuration would share worker
IDs, and one replica's `unregisterWorker` would mark the others' rows stopped. So for a
caller-supplied identity the SDK appends a per-instance random suffix to the
subscription worker ID: `<identity>#<6 random [a-z0-9]>/<task queue>`, for
example `orders-worker#k3x9qa/orders`. The Temporal client identity itself, and
the health probe matching on it, stay as configured. A generated identity
already ends in such a suffix and is used as is.

The service limits a worker ID to 255 bytes (the suffix counts); `Start`
fails with `ErrWorkerIDTooLong`, naming the task queue, if any ID would exceed it.

On start the worker registers only its own workflows and signals; it never lists
or deletes other workers' workflows (older versions pruned every workflow missing
from the local registry, which deleted other workers' workflows). A heartbeat
refreshes the subscriptions before their TTL lapses. A failed heartbeat is
retried after `PYCK_WORKER_REGISTRATION_HEARTBEAT_RETRY_BACKOFF`, doubling per
consecutive failure up to the heartbeat interval, so a worker re-registers soon
after an outage instead of waiting a whole interval; the normal interval resumes
after a success. On stop, the worker calls
`unregisterWorker` (once the heartbeat is cancelled), which marks its
subscriptions stopped. Nothing is deleted: the router ignores stopped
subscriptions on a workflow while another worker holds running ones (so an old
version stops routing once the new one has registered), and otherwise keeps
routing to them until their TTL lapses. A crash and a clean stop of the last
worker therefore both end at the TTL; a later registration (the heartbeat) clears
the mark. A failure is logged and shutdown continues. Stop therefore blocks for up to `PYCK_WORKER_UNREGISTER_TIMEOUT`
while unregistering, so keep it below the pod's termination grace period.

| Variable | Default | Purpose |
| --- | --- | --- |
| `PYCK_WORKER_REGISTRATION_HEARTBEAT_INTERVAL` | `5m` | Subscription refresh interval; must be below the service TTL (`PYCK_WORKFLOW_SUBSCRIPTION_TTL`, `1h`) |
| `PYCK_WORKER_REGISTRATION_HEARTBEAT_RETRY_BACKOFF` | `5s` | Wait before the first retry of a failed heartbeat (doubles, capped at the interval) |
| `PYCK_WORKER_REGISTRATION_RETRY_ATTEMPTS` | `5` | Startup registration attempts on transient conflicts |
| `PYCK_WORKER_REGISTRATION_RETRY_BACKOFF` | `1s` | Base backoff between those attempts |
| `PYCK_WORKER_UNREGISTER_ON_STOP` | `true` | Call `unregisterWorker` on Stop |
| `PYCK_WORKER_UNREGISTER_TIMEOUT` | `5s` | Bound on the `unregisterWorker` calls of one Stop (all IDs together) |

### Worker Deployment Versioning

A worker registers itself with Temporal under a **deployment version**
(`<deployment name>.<build ID>`) and runs with `Pinned` behaviour: an execution
stays on the version it started on until it finishes, so replacing the binary
under a running workflow can no longer replay changed code against an old
history.

| Variable | Default | Purpose |
| --- | --- | --- |
| `PYCK_WORKER_BUILD_ID` | Go module version | Explicit build ID override |
| `PYCK_WORKER_DEPLOYMENT_NAME` | Go module name | Explicit deployment name override |
| `TEMPORAL_WORKER_BUILD_ID` | — | Injected by the temporal-worker-controller |
| `TEMPORAL_DEPLOYMENT_NAME` | — | Injected by the temporal-worker-controller |
| `PYCK_WORKER_REQUIRE_BUILD_ID` | `false` | Refuse to start on an unversioned build |
| `PYCK_WORKER_PROMOTE_ON_START` | `false` | Promote own version once the workers poll; only without the controller |
| `PYCK_UI_BUNDLE_VERSION` | — | Version segment of the UI bundle URL |
| `PYCK_UI_BUNDLE_SLUG` | empty | Bundle slug, for shared flavour bundles only |

Build ID and deployment name resolve in that order: the `PYCK_` override first,
then whatever the controller injected, then the Go module.

The resolution lives in `common/workflow` (`VersioningConfig`), so the workers
embedded in services — management, file — register versions the same way
without adopting `RunDefaultWorker`, which owns a whole process. They read the
same variables; without a build ID they stay unversioned, since the service
images build without module version stamping.

A registered version receives no tasks until it is made *current*. Under the
temporal-worker-controller that is the controller's decision, gradual rollout
included. Elsewhere, `PYCK_WORKER_PROMOTE_ON_START=true` makes `RunDefaultWorker`
promote its own version through the Temporal API once its workers have started
(retrying until Temporal accepts it) — no `temporal` CLI in the deploy step. It
does nothing on an unversioned build. Never set it under the controller: a self-promoting worker
overrides the rollout it is in the middle of.

**Under Kubernetes, do not set the `PYCK_` overrides.** The controller derives
the deployment version itself and injects the `TEMPORAL_` pair; a `PYCK_` value
outranks it, so the worker would register under a version the controller is not
waiting for and the rollout would never complete. Pin the version on the CRD via
`spec.workerOptions.unsafeCustomBuildID` instead.

Locally, no configuration is needed: an unversioned (`go run`) build logs a
warning and runs without versioning. Set `PYCK_WORKER_REQUIRE_BUILD_ID=true` in
production so an unversioned worker fails loudly instead of silently opting out.

> A versioned worker receives **no tasks** until its version is promoted to
> current — by the controller in Kubernetes, or via
> `temporal worker deployment set-current-version` elsewhere. A worker that
> starts cleanly but sits idle is almost always an unpromoted version.

### UI bundles

Each worker stamps `ui.bundle.<WorkflowType>.{version,slug}` onto its own
deployment version at startup, for every workflow type it serves. The backend
reads it back off the version an execution is pinned to and renders the tenant's
URL template, so a running workflow always gets the UI that matches its code.

`PYCK_UI_BUNDLE_VERSION` is the version segment of that URL — the identifier the
bundle was uploaded under, typically a git SHA. It is deliberately not derived
from the build ID: the two coincide only when a deploy pins them together. Left
unset, the worker stamps nothing and the backend serves the configured default
bundle — the right answer for a deploy that named no bundle at all.

`PYCK_UI_BUNDLE_SLUG` names a shared bundle and is set only for workers serving
one. A per-tenant bundle needs no slug: the tenant already scopes the URL.

The worker exposes two HTTP endpoints on port `8080`
(`WithHealthServer(HealthPort(n))` to change it):

- **`/health`** — liveness. Ready or not, a live worker answers 200 here.
- **`/ready`** — readiness. 200 only once the UI bundle metadata is stamped, so
  wiring this as the pod's `readinessProbe` keeps a version from taking pinned
  executions before its bundle metadata exists.

## Worker Architecture

The worker automatically:

1. **Scans the registry** for all registered workflows
2. **Creates workers** based on unique task queues
3. **Registers workflows** with their configured names
4. **Calls Setup()** to get activity structs (with shared state)
5. **Registers activities** on the appropriate workers
6. **Starts all workers** concurrently
7. **Handles graceful shutdown** on SIGTERM/SIGINT (unregisters its subscriptions, then stops the workers)

Multiple workflows can share a worker if they use the same task queue, or they can be isolated by using different queues.

## Related Packages

- **[workflowgen](../workflowgen/README.md)**: Code generation tool for automatic main.go creation
- **[backend/common/workflow](../common/workflow)**: Shared workflow types and constants
- **[backend/common/events](../common/events)**: Event bus integration for signals
