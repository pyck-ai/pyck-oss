# workflowgen

A Go code generation tool that automatically discovers workflow packages with `init()` functions and generates a `main.go` file that imports them. This eliminates the need to manually maintain import lists when adding new workflows to your Temporal worker.

## Overview

In the Pyck platform, workflows register themselves via `init()` functions that call `workflowsdk.MustRegisterWorkflow()`. The `workflowgen` tool scans your codebase to find all workflow packages and generates the necessary imports, ensuring your worker automatically loads all workflows without manual intervention.

### Safe, Non-Destructive Updates

`workflowgen` is designed to work alongside your custom code:
- **Only updates underscore imports**: Changes the `_ "workflow/package"` import section
- **Preserves everything else**: Your `main()` function, custom worker configuration, imports, and comments stay intact
- **Freely customizable**: Modify `main.go` however you want—add custom startup logic, change worker options, add middleware, etc.
- **Incremental sync**: Re-run `go generate` anytime to sync imports with your current workflow files

## How It Works

1. **Scans directories** (recursively or non-recursively) for Go packages
2. **Parses Go files** using the `go/ast` package to find `init()` functions
3. **Generates import paths** relative to your module root
4. **Non-destructively updates `main.go`**: Only modifies the underscore import section for auto-detected workflows
5. **Preserves all your custom code**: Your `main()` function, custom imports, comments, and other code remain untouched
6. **Syncs workflow imports**: Automatically adds new workflows and removes deleted ones
7. **Reports changes**: Shows exactly which imports were added or removed

**Important**: `workflowgen` DOES NOT override your `main.go` file. It only updates the underscore imports for workflows, so you can freely customize worker configuration, add logging, modify startup logic, etc.

## Installation

Build and install the tool:

```bash
cd backend/workflowgen
go install
```

Or use it via `go run`:

```bash
go run github.com/pyck-ai/pyck/backend/workflowgen ./workflows/...
```

## Usage

### Basic Usage

```bash
# Scan a directory recursively
workflowgen ./workflows/...

# Scan multiple directories
workflowgen ./workflows/... ./services/...

# Scan a single directory (non-recursive)
workflowgen ./workflows

# Enable verbose output
workflowgen -v ./workflows/...
```

### Command-Line Options

- **`-v`**: Enable verbose output showing discovered packages and changes
- **`<path>...`**: One or more directory paths or patterns
  - `./path/...` - Recursive search (includes subdirectories)
  - `./path` - Non-recursive search (single directory only)

### Using with `go:generate`

Add a `go:generate` directive to your `main.go`:

```go
//go:generate go tool workflowgen ./workflows/...

package main

import (
    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"
)

func main() {
    workflowsdk.RunDefaultWorker()
}
```

Then run:

```bash
go generate
```

This automatically updates your imports whenever you run `go generate`.

## Example Workflow Structure

```
myservice/
├── go.mod                           # module github.com/pyck-ai/pyck/backend/myservice
├── main.go                          # Worker entry point
└── workflows/
    ├── dataprocessor/
    │   ├── workflow.go
    │   ├── activities.go
    │   └── init.go              # Registers workflow
    └── notification/
        ├── workflow.go
        ├── activities.go
        └── init.go              # Registers workflow
```

### Workflow Registration (`init.go`)

```go
package dataprocessor

import workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"

func init() {
    workflowsdk.MustRegisterWorkflow(&DataProcessorWorkflow{})
}
```

### Running workflowgen

```bash
cd myservice
workflowgen -v ./workflows/...
```

**Output:**
```
Found 2 package(s) with init() functions:
  - github.com/pyck-ai/pyck/backend/myservice/workflows/dataprocessor
  - github.com/pyck-ai/pyck/backend/myservice/workflows/notification

Added 2 import(s):
  + github.com/pyck-ai/pyck/backend/myservice/workflows/dataprocessor
  + github.com/pyck-ai/pyck/backend/myservice/workflows/notification

Successfully updated main.go
```

### Generated `main.go`

```go
//go:generate go tool workflowgen ./workflows/...

package main

import (
    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"

    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/dataprocessor"
    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/notification"
)

func main() {
    workflowsdk.RunDefaultWorker()
}
```

## Custom Code Preservation Example

`workflowgen` intelligently updates only the workflow imports, leaving all your customizations intact.

**Before running workflowgen (custom main.go):**

```go
//go:generate go tool workflowgen ./workflows/...

package main

import (
    "context"
    "log"
    "os"

    pycklog "github.com/pyck-ai/pyck/backend/common/log"
    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"
    temporalworker "go.temporal.io/sdk/worker"

    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/dataprocessor"
    // Missing: notification workflow (will be added automatically)
)

func main() {
    ctx := context.Background()

    if err := workflowsdk.LoadEnv(ctx); err != nil {
        log.Fatal(err)
    }

    // Custom worker configuration
    worker, err := workflowsdk.NewWorker(
        workflowsdk.WithWorkerOptions(temporalworker.Options{
            MaxConcurrentActivityExecutionSize: 200,
        }),
    )
    if err != nil {
        log.Fatal(err)
    }

    defer worker.Stop()

    if err := worker.Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

**After running `go generate` (only imports updated):**

```go
//go:generate go tool workflowgen ./workflows/...

package main

import (
    "context"
    "log"
    "os"

    pycklog "github.com/pyck-ai/pyck/backend/common/log"
    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"
    temporalworker "go.temporal.io/sdk/worker"

    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/dataprocessor"
    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/notification"  // ← Added automatically
)

func main() {
    ctx := context.Background()

    if err := workflowsdk.LoadEnv(ctx); err != nil {
        log.Fatal(err)
    }

    // Custom worker configuration - COMPLETELY PRESERVED
    worker, err := workflowsdk.NewWorker(
        workflowsdk.WithWorkerOptions(temporalworker.Options{
            MaxConcurrentActivityExecutionSize: 200,
        }),
    )
    if err != nil {
        log.Fatal(err)
    }

    defer worker.Stop()

    if err := worker.Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

**What changed**: Only the underscore import section. Your custom `main()` function, imports, and configuration remain untouched.

## How Import Organization Works

The tool organizes imports into three sections:

1. **Standard library imports** (packages without dots in their path)
2. **Third-party imports** (external packages with dots in their path)
3. **Underscore imports** (auto-generated workflow packages)

Blank lines separate each section for readability.

### Before workflowgen

```go
import (
    "context"
    "log"

    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"
)
```

### After workflowgen

```go
import (
    "context"
    "log"

    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"

    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/dataprocessor"
    _ "github.com/pyck-ai/pyck/backend/myservice/workflows/notification"
)
```

## Template File

When `main.go` doesn't exist, `workflowgen` creates it from an embedded template ([main.go.tmpl](./main.go.tmpl)):

```go
//go:generate go tool workflowgen ./workflows/...

package main

import (
    workflowsdk "github.com/pyck-ai/pyck/backend/workflowsdk"
)

func main() {
    if err := workflowsdk.RunDefaultWorker("default"); err != nil {
        log.Fatal(err)
    }
}

// Alternatively, you can customize your worker like this:
//
// [... example code showing custom worker setup ...]
```

This template includes:
- **go:generate directive** for easy regeneration
- **RunDefaultWorker()** for zero-config startup
- **Commented example** showing custom worker configuration

## Advanced Usage

### Integration with Taskfile

Add to your `Taskfile.yaml`:

```yaml
version: '3'

tasks:
  generate:
    desc: Generate workflow imports
    cmds:
      - go run github.com/pyck-ai/pyck/backend/workflowgen ./workflows/...

  build:
    desc: Build worker binary
    deps:
      - generate
    cmds:
      - go build -o worker .
```

Run:

```bash
task generate
task build
```

### Integration with CI/CD

Ensure generated files are up-to-date in CI:

```bash
# Generate imports
go run github.com/pyck-ai/pyck/backend/workflowgen ./workflows/...

# Check for uncommitted changes
git diff --exit-code main.go || {
    echo "Error: main.go is out of sync. Run 'workflowgen ./workflows/...' locally."
    exit 1
}
```

### Scanning Multiple Patterns

Different workflow locations can be scanned in one command:

```bash
workflowgen \
    ./workflows/... \
    ./internal/workflows/... \
    ./services/*/workflows/...
```

This finds all packages with `init()` functions across multiple directory trees.

## How It Detects `init()` Functions

The tool uses Go's AST parser to scan each `.go` file (excluding `*_test.go`) and looks for:

```go
func init() {
    // ...
}
```

Only top-level `init()` functions are detected (not methods or nested functions). Once found in any file within a package, that package's import path is added.

## Error Handling

The tool handles several error scenarios:

| Error | Description | Solution |
|-------|-------------|----------|
| `module declaration not found in go.mod` | No `go.mod` file or missing `module` directive | Ensure `go.mod` exists with valid module path |
| `failed to read directory` | Directory doesn't exist or permission denied | Check path spelling and file permissions |
| `error parsing <file>` | Invalid Go syntax in file | Fix syntax errors (tool continues to other files) |
| `failed to read main.go` | Permission or I/O error reading `main.go` | Check file permissions |
| `failed to write main.go` | Permission or I/O error writing `main.go` | Check file permissions and disk space |

Parsing errors for individual files are logged as warnings when `-v` is enabled, but don't stop the tool from processing other files.

## Design Decisions

### Why Underscore Imports?

Underscore imports (`import _ "package"`) execute a package's `init()` functions without requiring direct references to the package. This is the standard Go pattern for side-effect imports, perfect for workflow registration.

### Why AST Parsing?

The tool uses `go/ast` to parse Go source files rather than using reflection or runtime discovery. This allows:
- **Build-time generation** instead of runtime discovery
- **No runtime overhead** from scanning packages
- **Type-safe imports** verified at compile time
- **IDE support** for generated imports

### Why Not Just Import All Packages?

Automatically importing all packages would:
- Include test files and unused code
- Increase binary size unnecessarily
- Make it unclear which code is actually used

By scanning for `init()` functions, we only import packages that explicitly register workflows.

## Troubleshooting

### Workflow Not Registered

If a workflow isn't being loaded:

1. **Verify `init()` exists**: Check that the package has an `init()` function
2. **Run with `-v`**: See if the package is detected:
   ```bash
   workflowgen -v ./workflows/...
   ```
3. **Check file name**: Ensure it's not a test file (`*_test.go`)
4. **Verify import path**: Confirm the import in `main.go` matches your module path

### Import Already Exists

If the tool reports "No changes needed" but imports are missing:

1. Check if imports are in a different format (e.g., aliased)
2. Ensure `main.go` has proper Go syntax
3. Try deleting `main.go` and regenerating from template

### Permission Denied

If you get permission errors:

```bash
chmod 644 main.go
workflowgen ./workflows/...
```

---

# RFW Component Generation

In addition to Go workflow import generation, `workflowgen` automatically generates RFW (Remote Flutter Widgets) components from BPMN files. This feature parses BPMN user tasks and automatically creates/updates corresponding RFW component files.

## Overview

When run, workflowgen automatically:

1. **Generates Go workflow imports** (as described above)
2. **Parses BPMN files** to find user tasks with component ID annotations
3. **Generates RFW component scaffolds** for annotated user tasks only
4. **Removes orphaned components** (annotations deleted from BPMN)
5. **Preserves existing components** (annotations still in BPMN)
6. **Stitches all components** into a single `dist/widgets.rfwtxt` file

**BPMN is the source of truth**: Only user tasks with "ID: ComponentName" text annotations are processed. Component IDs come from the component ID specified in the annotation text.

## Usage

### Basic Usage

```bash
# Generate Go workflow imports and RFW components
workflowgen ./workflows/...

# With verbose output
workflowgen -v ./workflows/...

# Dry-run (preview without writing)
workflowgen --dry-run ./workflows/...
```

### Using with `go:generate`

Add to your `main.go`:

```go
//go:generate go tool workflowgen ./workflows/...

package main
// ...
```

Then run:

```bash
go generate
```

This will:
1. Generate Go workflow imports
2. Generate/update RFW components from BPMN
3. Stitch all components into `dist/widgets.rfwtxt`

## Workflow Structure for RFW

```
workflows/
├── 101-unloading/
│   ├── 101-unloading.bpmn           # BPMN source (contains user tasks)
│   ├── flutter/
│   │   └── index.rfwtxt             # Auto-generated RFW components
│   └── temporal/
│       └── workflow.go
├── 110-quality-inspection/
│   ├── 110-quality-inspection.bpmn
│   └── flutter/
│       └── index.rfwtxt
└── dist/
    └── widgets.rfwtxt                # Stitched output (all workflows)
```

## BPMN User Tasks → RFW Components

### BPMN Example with Component ID Annotation

To generate an RFW component, add a text annotation with "ID: ComponentName" and associate it with the user task:

```xml
<!-- User Task -->
<bpmn:userTask id="Activity_0uj4ej4" name="Scan Barcode">
  <bpmn:extensionElements>
    <zeebe:userTask />
  </bpmn:extensionElements>
</bpmn:userTask>

<!-- Text Annotation (text content specifies the component ID) -->
<bpmn:textAnnotation id="Annotation_123">
  <bpmn:text>ID: ScanBarcodeWidget</bpmn:text>
</bpmn:textAnnotation>

<!-- Association linking user task to annotation -->
<bpmn:association sourceRef="Activity_0uj4ej4" targetRef="Annotation_123" />
```

### Generated RFW Component

The tool generates an RFW component scaffold in `workflows/101-unloading/flutter/index.rfwtxt`:

```
// User Task: Scan Barcode
// Source: workflows/101-unloading/101-unloading.bpmn
widget ScanBarcodeWidget = ActivityContainer(
    child: ListView(
        children: [
          ActivityHeader(title: "Scan Barcode"),
          // TODO: Implement UI for Scan Barcode
          OutlinedButton(text: "Next [DEBUG]", onPressed: event "next" { }),
        ]
    )
);
```

**Important**:
- Component ID (`ScanBarcodeWidget`) comes from the **annotation text content** after "ID: "
- Only user tasks with "ID: ComponentName" annotations are processed
- User tasks without this annotation are ignored completely
- The annotation's XML id attribute (e.g., `Annotation_123`) is not used
- RFW syntax uses a declarative format similar to Flutter, but stored as text

## Component Synchronization

### Adding an RFW Component

1. **Add user task to BPMN** (e.g., in Camunda Modeler)
2. **Add text annotation** containing "ID: YourComponentName" (e.g., `ID: ScanBarcodeWidget`)
3. **Add association** linking the user task to the annotation
4. **Run** `workflowgen ./workflows/...`
5. **Result**: New RFW component scaffold created using the component ID

### Removing an RFW Component

1. **Delete the text annotation** or **remove the association** in BPMN
2. **Run** `workflowgen ./workflows/...`
3. **Result**: Corresponding component removed from `flutter/index.rfwtxt` (use git to recover if needed)

### Renaming a Component ID

1. **Change annotation text in BPMN** (e.g., `ID: ScanBarcode` → `ID: ScanContainer`)
2. **Run** `workflowgen ./workflows/...`
3. **Result**: Old component deleted, new component scaffold created

**Warning**: Renaming loses custom component implementation. Copy implementation manually if needed.

### Updating Component Title (Task Name)

1. **Change user task name in BPMN** (e.g., "Scan Barcode" → "Scan Container Barcode")
2. **Run** `workflowgen ./workflows/...`
3. **Result**: Component implementation preserved, only the title comment is updated

### Making a User Task Backend-Only

1. **Delete the annotation** or **change the text** to remove "ID: " prefix
2. **Run** `workflowgen ./workflows/...`
3. **Result**: RFW component is removed (user task remains in BPMN for backend processing only)

## Final Stitched Output

The `dist/widgets.rfwtxt` file combines all workflow components:

```
// Code generated by pyck-ai/backend/workflowgen, DO NOT EDIT.

import core.widgets;
import local;

// Workflow: 101-unloading (4 components)
// Source BPMN: workflows/101-unloading/101-unloading.bpmn
// Source RFW: workflows/101-unloading/flutter/index.rfwtxt
widget ScanContainer = ...
widget ConfirmItems = ...
widget SelectLocation = ...
widget CompleteUnloading = ...

// Workflow: 110-quality-inspection (7 components)
// Source BPMN: workflows/110-quality-inspection/110-quality-inspection.bpmn
// Source RFW: workflows/110-quality-inspection/flutter/index.rfwtxt
widget StartInspection = ...
// ... more components

// Workflow: 120-putaway (3 components)
// Source BPMN: workflows/120-putaway/120-putaway.bpmn
// Source RFW: workflows/120-putaway/flutter/index.rfwtxt
widget SelectBin = ...
// ... more components
```

This file can be:
- Uploaded to CDN for mobile app consumption
- Included in Flutter build
- Used for documentation/testing

## Integration with Taskfile

Add to your build process:

```yaml
version: '3'

tasks:
  generate:
    desc: Generate Go workflow imports and RFW components
    cmds:
      - go run github.com/pyck-ai/pyck/backend/workflowgen -v ./workflows/...

  build:
    desc: Build worker binary
    deps:
      - generate
    cmds:
      - go build -o worker .
```

## RFW Component Development Workflow

### 1. Design BPMN Process
- Create/update BPMN file in Camunda Modeler
- Add user tasks for workflow steps
- Add text annotations containing "ID: ComponentName" for UI screens
- Associate annotations with user tasks (drag association from task to annotation)
- Use meaningful component IDs (e.g., `ID: ScanBarcodeWidget`, not generic names)
- Give descriptive task names (becomes component title)

### 2. Generate Component Scaffolds
```bash
workflowgen -v ./workflows/...
```

### 3. Implement Components
- Open `workflows/*/flutter/index.rfwtxt`
- Replace `// TODO` comments with actual RFW declarations
- Use RFW declarative syntax (ActivityContainer, ListView, etc.)

### 4. Test & Iterate
- Make BPMN changes as needed
- Re-run workflowgen (preserves your implementations)
- Use git diff to see changes

### 5. Deploy
```bash
# Generate and stitch for production
workflowgen ./workflows/...

# Upload dist/widgets.rfwtxt to CDN
# (deployment step depends on your infrastructure)
```

## Example: Full Workflow

**Step 1: Create BPMN** (`workflows/101-unloading/101-unloading.bpmn`)

```xml
<bpmn:process id="Process_101">
  <bpmn:userTask id="Activity_scan" name="Scan Container"/>
  <bpmn:userTask id="Activity_confirm" name="Confirm Items"/>

  <!-- Component ID Annotations -->
  <bpmn:textAnnotation id="Annotation_1">
    <bpmn:text>ID: ScanContainerWidget</bpmn:text>
  </bpmn:textAnnotation>
  <bpmn:association sourceRef="Activity_scan" targetRef="Annotation_1"/>

  <bpmn:textAnnotation id="Annotation_2">
    <bpmn:text>ID: ConfirmItemsWidget</bpmn:text>
  </bpmn:textAnnotation>
  <bpmn:association sourceRef="Activity_confirm" targetRef="Annotation_2"/>
</bpmn:process>
```

**Step 2: Generate**

```bash
$ workflowgen -v ./workflows/...

Processing ./workflows/... for RFW components...
Found 1 BPMN file(s):
  - workflows/101-unloading/101-unloading.bpmn

Found 2 user task(s) in workflows/101-unloading/101-unloading.bpmn:
  - ScanContainerWidget: Scan Container
  - ConfirmItemsWidget: Confirm Items

Syncing workflows/101-unloading/flutter/index.rfwtxt with workflows/101-unloading/101-unloading.bpmn...
  Adding 2 component(s):
    + ScanContainerWidget (Scan Container)
    + ConfirmItemsWidget (Confirm Items)

Summary: added 2, removed 0 RFW component(s)

Stitching RFW components from ./workflows/...
Found 1 RFW component file(s) to stitch
  - 101-unloading: 2 component(s)

Successfully generated dist/widgets.rfwtxt
  Total workflows: 1
  Total components: 2
```

**Step 3: Result** (`workflows/101-unloading/flutter/index.rfwtxt`)

```
import core.widgets;
import local;

// User Task: Scan Container
// Source: workflows/101-unloading/101-unloading.bpmn
widget ScanContainerWidget = ActivityContainer(
    child: ListView(
        children: [
          ActivityHeader(title: "Scan Container"),
          // TODO: Implement UI for Scan Container
          OutlinedButton(text: "Next [DEBUG]", onPressed: event "next" { }),
        ]
    )
);

// User Task: Confirm Items
// Source: workflows/101-unloading/101-unloading.bpmn
widget ConfirmItemsWidget = ActivityContainer(
    child: ListView(
        children: [
          ActivityHeader(title: "Confirm Items"),
          // TODO: Implement UI for Confirm Items
          OutlinedButton(text: "Next [DEBUG]", onPressed: event "next" { }),
        ]
    )
);
```

**Step 4: Implement** (developer edits `index.rfwtxt`)

```
import core.widgets;
import local;

// User Task: Scan Container
// Source: workflows/101-unloading/101-unloading.bpmn
widget ScanContainerWidget = ActivityContainer(
    child: ListView(
        children: [
          ActivityHeader(title: "Scan Container"),
          ScanningPill(text: "Scan a Container Label"),
          SearchBar(searchHintText: "Or search manually"),
        ]
    )
);

// ... ConfirmItemsWidget implementation
```

**Step 5: Re-generate** (preserves implementation)

```bash
$ workflowgen ./workflows/...
# Implementation preserved, only metadata updated
```

## Troubleshooting RFW Mode

### Component Not Generated

1. **Check BPMN syntax**: Ensure valid BPMN 2.0 XML
2. **Verify component ID annotation**: Must have `<bpmn:textAnnotation>` with "ID: ComponentName" text
3. **Verify association**: User task must be linked to annotation via `<bpmn:association>`
4. **Run verbose**: `workflowgen -v ./workflows/...`
5. **Check file permissions**: Ensure flutter/ directory is writable

### Component Unexpectedly Removed

- Check BPMN file: "ID: " annotation or association may have been deleted
- Verify annotation still contains "ID: " prefix followed by component name
- Use git to recover: `git checkout workflows/*/flutter/index.rfwtxt`
- Restore and re-run workflowgen

### Stitched File Not Updated

- Check dist/ directory exists and is writable
- Verify flutter/index.rfwtxt files exist and have components
- Re-run `workflowgen ./workflows/...`

### BPMN Parse Errors

```
Error: failed to parse BPMN XML in workflows/101-unloading/101-unloading.bpmn
```

- Open BPMN in Camunda Modeler and verify no XML errors
- Ensure file is valid BPMN 2.0 format
- Check file encoding (must be UTF-8)

## Related Packages

- **[workflowsdk](../workflowsdk/README.md)**: SDK for building Temporal workflows
- **[backend/common/workflow](../common/workflow)**: Shared workflow types
