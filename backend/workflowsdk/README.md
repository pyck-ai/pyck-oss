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
            events.MutationEventWithReplyTopic{
                TenantID:      tenantID,
                ServiceName:   "data",
                OperationName: "created",
            },
        ),
        workflowsdk.NewIntermediateSignal(
            events.MutationEventWithReplyTopic{
                TenantID:      tenantID,
                ServiceName:   "approval",
                OperationName: "received",
            }, "approval-received",
        ),
    }
}
```

Signal types:
- **Start**: Triggers workflow creation
- **Intermediate**: Sent to running workflows


> **Note on Sparse Topic Structs**:
> - **Omitted fields become wildcards**: Only set the fields you need to match. For example, omitting `SchemaName` will match events from any schema.
> - **Tenant ID is special**: If `TenantID` is not set (or set to zero), it will be automatically replaced with ALL tenant IDs the current user is authorized for. This ensures proper multi-tenant isolation.
> - **Available fields**: `MutationEventWithReplyTopic` supports `TenantID`, `ServiceName`, `SchemaName`, `EntityID`, and `OperationName`.
> - **Other topic types**: See [backend/common/events](../common/events) for `CustomEventTopic`, `WorkflowEventTopic`, etc.
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

- **`PYCK_API_TOKEN`**: Authentication token for Pyck API
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

## Worker Architecture

The worker automatically:

1. **Scans the registry** for all registered workflows
2. **Creates workers** based on unique task queues
3. **Registers workflows** with their configured names
4. **Calls Setup()** to get activity structs (with shared state)
5. **Registers activities** on the appropriate workers
6. **Starts all workers** concurrently
7. **Handles graceful shutdown** on SIGTERM/SIGINT

Multiple workflows can share a worker if they use the same task queue, or they can be isolated by using different queues.

## Related Packages

- **[workflowgen](../workflowgen/README.md)**: Code generation tool for automatic main.go creation
- **[backend/common/workflow](../common/workflow)**: Shared workflow types and constants
- **[backend/common/events](../common/events)**: Event bus integration for signals
