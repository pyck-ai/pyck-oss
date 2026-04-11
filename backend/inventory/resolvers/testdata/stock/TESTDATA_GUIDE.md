# Stock Test Scenario Guide

This document defines the conventions for writing YAML-based stock test scenarios in this directory. All `*.test.yaml` files are loaded by `TestStockPlausibility` in `stocks_test.go` and executed as table-driven integration tests.

## File Structure

Each `.test.yaml` file follows this structure:

```yaml
# <Title>
# <Description of what the test validates>
name: <kebab-case-test-name>
itemSKU: <unique-item-sku>

# ========================================================================
# REPOSITORY STRUCTURE OVERVIEW
# ========================================================================
#
#  <tree diagram showing parent-child relationships>
#
# ========================================================================

repositories:
  # <comment describing role>
  - name: <repo-name>
    parent: "<parent-name-or-empty>"
    type: static|dynamic
    virtual: true|false

steps:
  # <graph comment>
  - name: <step_name>
    action: create|execute|delete
    ...
    expectedStocks:
      <repo-name>: { qty: N, ownQty: N, in: N, ownIn: N, out: N, ownOut: N }
```

## Top-Level Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Unique test name (kebab-case), used as Go subtest name |
| `itemSKU` | Yes* | SKU for the item created in this test (must be unique across files) |
| `items` | Yes* | List of item SKUs for multi-item scenarios (mutually exclusive with `itemSKU`) |
| `repositories` | Yes | List of repositories to create before steps execute |
| `steps` | Yes | Ordered list of movement actions and expected stock states |

\* Either `itemSKU` (single item) or `items` (multi-item list) must be provided, but not both. When `items` is used, the first item in the list becomes the default item for steps that don't specify an `item` field.

## Repository Structure Overview

Every file **must** include a repository structure overview comment block between the `itemSKU` and `repositories` fields. The overview shows **only the initial tree structure** before any steps execute:

```yaml
# ========================================================================
# REPOSITORY STRUCTURE OVERVIEW
# ========================================================================
#
#  warehouse (root)
#  ├─ shelf
#  ├─ zone-a
#  └─ zone-b
#
# ========================================================================
```

Rules:
- Use `├─` for intermediate children and `└─` for the last child
- Use `│` for vertical continuation lines
- Mark root repositories with `(root)` suffix
- Show the tree **before** any steps execute (initial state only)
- Multi-level nesting uses `│  ` (pipe + two spaces) for indentation
- Do **not** include "After" or post-movement structure diagrams

## Repository Definitions

Each repository entry **must** have a descriptive inline comment explaining its role:

```yaml
repositories:
  # Virtual repository for initial stock additions
  - name: virtual
    parent: ""
    type: static
    virtual: true

  # Main warehouse container
  - name: warehouse
    parent: ""
    type: static
    virtual: false

  # Shelf - holds initial stock, source for outbound movements
  - name: shelf
    parent: warehouse
    type: static
    virtual: false
```

## Step Types

### Create Item Movement (`action: create`, `moveType: item`)

Creates a pending item movement between two repositories.

```yaml
  - name: step_name
    action: create
    moveType: item
    from: <source-repo>
    to: <destination-repo>
    qty: <quantity>
    expectedStocks: { ... }
```

### Create Repository Movement (`action: create`, `moveType: repository`)

Creates a pending repository movement (changes parent-child relationship).

```yaml
  - name: step_name
    action: create
    moveType: repository
    from: <repo-being-moved>
    to: <new-parent-repo>
    expectedStocks: { ... }
```

### Create Collection Movement (`action: create`, `moveType: collection`)

Creates a batch of movements in a single atomic operation. Supports mixed types (item and repository movements in the same collection). **Collections must always use the explicit `collection:` array format**, even for single-entry collections. Do not use the shorthand format with top-level `from`/`to`/`qty` fields.

```yaml
  - name: step_name
    action: create
    moveType: collection
    collection:
      - from: <source>
        to: <destination>
        qty: <quantity>
        moveType: item          # optional, "item" is default
      - from: <repo-to-move>
        to: <new-parent>
        moveType: repository
    expectedStocks: { ... }
```

Collection entries are referenced by positional index: `step_name[0]`, `step_name[1]`, etc.

### Execute Movement (`action: execute`)

Executes a previously created (pending) movement.

```yaml
  - name: step_name
    action: execute
    movement: <reference-to-create-step>
    expectedStocks: { ... }
```

The `movement` field references either a step name (e.g., `setup_seed`) or a collection position (e.g., `create_collection[0]`).

### Delete Movement (`action: delete`)

Soft-deletes a previously created movement.

```yaml
  - name: step_name
    action: delete
    movement: <reference-to-create-step>
    expectedStocks: { ... }
```

### Expected Errors

Any step can include `expectError` to assert failure:

```yaml
  - name: step_name
    action: create
    moveType: item
    from: source
    to: destination
    qty: 999
    expectError: "insufficient stock"
    expectedStocks: { ... }
```

Steps with `expectError` do **not** get graphs — the error means no state change occurs.

### Multi-Item Step Fields

When using `items` (multi-item mode), steps gain additional fields:

| Field | Required | Description |
|-------|----------|-------------|
| `item` | No | Which item SKU this step operates on (defaults to first item in the `items` list) |
| `expectedItemStocks` | No | Per-item stock assertions (use instead of `expectedStocks` in multi-item scenarios) |

The `item` field selects which item to use for item movement create/execute/delete operations. Repository movements operate on all items in the repository regardless of this field.

`expectedItemStocks` maps item SKU → repository name → stock levels:

```yaml
expectedItemStocks:
  item-a:
    warehouse: { qty: 100, ownQty: 0, in: 0, ownIn: 0, out: 0, ownOut: 0 }
    shelf:     { qty: 100, ownQty: 100, in: 0, ownIn: 0, out: 0, ownOut: 0 }
  item-b:
    warehouse: { qty: 50, ownQty: 0, in: 0, ownIn: 0, out: 0, ownOut: 0 }
    shelf:     { qty: 50, ownQty: 50, in: 0, ownIn: 0, out: 0, ownOut: 0 }
```

Both `expectedStocks` and `expectedItemStocks` can be used in the same step. `expectedStocks` validates against the step's resolved item (from `item` field or default), while `expectedItemStocks` validates each specified item independently.

## Stock Level Fields

Each `expectedStocks` entry maps a repository name to its expected stock state:

```yaml
expectedStocks:
  repo-name: { qty: N, ownQty: N, in: N, ownIn: N, out: N, ownOut: N }
```

| Field | YAML Key | Description |
|-------|----------|-------------|
| Quantity | `qty` | Aggregated quantity = own + all descendants' own quantities |
| Own Quantity | `ownQty` | Items physically stored directly in this repository |
| Incoming | `in` | Pending incoming stock (aggregated across subtree) |
| Own Incoming | `ownIn` | Pending incoming stock directly at this repository |
| Outgoing | `out` | Pending outgoing stock (aggregated across subtree) |
| Own Outgoing | `ownOut` | Pending outgoing stock directly at this repository |

### Stock Behavior Rules

1. **Creating** an item movement adds reservations: `ownOut` on source, `ownIn` on destination
2. **Executing** an item movement converts reservations to actual quantities: clears `in`/`out`, updates `qty`/`ownQty`
3. **Deleting** a pending movement removes its reservations
4. **Creating** a repository movement adds aggregated reservations: `out` on the current parent of the moved repo (not `ownOut`), `in` on the destination repo (not `ownIn`). The moved repo itself is not directly affected.
5. **Executing** a repository movement changes the tree structure: the moved repo becomes a child of the new parent, and stock aggregation recalculates accordingly
6. **Aggregation** flows up the tree: a parent's `qty` = sum of all descendants' `ownQty` values; same for `in` and `out`
7. **Conservation**: total items in the system are constant — `warehouse.qty` always equals the sum of all leaf `ownQty` values under it

## ASCII Graph Conventions

### General Rules

- Graphs are YAML comments (prefixed with `#`) placed **immediately before** the step or collection entry they describe
- Use `# =========================================================================` separator lines to frame each graph
- Every `create` step **must** have a graph; `delete` steps **must** have a deletion graph
- `execute` steps do **not** get graphs
- Steps with `expectError` do **not** get graphs
- Inside multi-entry collections, each entry gets its own graph

### Section Headers

Use separator blocks with descriptive text to group related test phases:

```yaml
  # =========================================================================
  # Cycle 1: Create → Delete → Recreate with different qty → Execute
  # =========================================================================
```

or

```yaml
  # =========================================================================
  # TEST 2: Create and delete pending repository movement
  # =========================================================================
```

### Item Movement Graphs (Between Sibling Repositories)

For item movements within the same tree, show the relevant subtree with box-drawing arrows routing from source to destination:

```yaml
  # =========================================================================
  #  warehouse
  #  ├─ shelf ──[40 items]─╮
  #  ├─ zone-a ◄───────────╯
  #  ├─ zone-b
  #  └─ zone-c
  # =========================================================================
```

When the destination is further down in the tree, use vertical pipe `│` to route the arrow:

```yaml
  # =========================================================================
  #  warehouse
  #  ├─ shelf ──[30 items]──╮
  #  ├─ zone-a              │
  #  ├─ zone-b ◄────────────╯
  #  └─ zone-c
  # =========================================================================
```

For movements between repositories at different tree depths:

```yaml
  # =========================================================================
  #  warehouse
  #  ├─ zone-a
  #  │  └─ shelf ──[20 items]─╮
  #  └─ zone-b ◄──────────────╯
  # =========================================================================
```

For deep hierarchies, show the relevant subtree with annotation:

```yaml
  # =========================================================================
  #  A_warehouse
  #  └─ A1_storage
  #     ├─ A1_1_zone
  #     │  └─ A1_1_1_shelf ──[30 items]──╮
  #     │                                │ (sibling zones)
  #     └─ A1_2_zone                     │
  #        └─ pallet ◄───────────────────╯
  # =========================================================================
```

### Item Movement Graphs (Cross-Tree / Virtual Source)

For seed movements from a virtual repository into the tree, show the source on the left and the destination tree on the right:

```yaml
  # =========================================================================
  #  virtual                             warehouse
  #    └─[50 items]─────────────────────► └─ shelf
  # =========================================================================
```

For deeper destination trees:

```yaml
  # =========================================================================
  #  virtual                             warehouse
  #  │                                   └─ storage
  #  └─[100 items]────────────────────────► └─ shelf
  # =========================================================================
```

### Repository Movement Graphs

**Side-by-side format** — For repo movements that restructure the tree, show the before and after trees as two columns:

```yaml
      # =========================================================================
      #  warehouse                           warehouse
      #  ├─ zone-a                           ├─ zone-a
      #  ├─ zone-b  ╭──[repo]──────────────► │  └─ ▒▒▒▒▒▒▒▒ (shelf1)
      #  └─ shelf1 ─╯                        └─ zone-b
      # =========================================================================
```

- `[repo]` label on the arrow indicates a structural/tree movement (not item transfer)
- `▒▒▒▒▒▒▒▒ (name)` highlights the moved repository in its new position

**Single-tree format** — For repo movements within the same tree where the arrow fits in one diagram:

```yaml
  # =========================================================================
  #  warehouse
  #  ├─ storage
  #  │  └─ shelf ────[repo]─╮
  #  ├─ ▒▒▒▒▒▒▒▒ ◄──────────╯
  #  └─ outbound
  # =========================================================================
```

The `▒▒▒▒▒▒▒▒` marks where the repo will land after execution.

### Deletion Graphs

For movement deletions, show the same graph as the original creation but with `╳` marking the cancellation and `(delete movement)` annotation:

```yaml
  # =========================================================================
  #  warehouse
  #  ├─ shelf ──[40 items]─╳ (delete movement)
  #  └─ outbound ◄─────────╯
  # =========================================================================
```

When the destination is further down in the tree:

```yaml
  # =========================================================================
  #  warehouse
  #  ├─ shelf ──[15 items]──╮
  #  ├─ zone-a              ╳ (delete movement)
  #  ├─ zone-b ◄────────────╯
  #  └─ outbound
  # =========================================================================
```

The `╳` replaces a `│` at the cancellation point. The destination arrow `◄────╯` is still shown to identify which movement is being cancelled.

### Box-Drawing Character Reference

| Character | Usage |
|-----------|-------|
| `├─` | Tree branch (intermediate child) |
| `└─` | Tree branch (last child) |
| `│` | Vertical tree/arrow continuation |
| `──` | Horizontal arrow/connection |
| `►` | Arrow head (right, into destination) |
| `◄` | Arrow head (left, into destination) |
| `╮` | Arrow corner (right to down) |
| `╯` | Arrow corner (up to left) |
| `╭` | Arrow corner (down to right) |
| `╳` | Deleted/cancelled movement |
| `▒` | Highlighted/moved repository |

### Alignment Rules

1. Within a single graph, the `╮`, `│`, and `╯` characters **must** be vertically aligned in the same column
2. The `◄` or `►` arrow heads connect to the `╯` or `╮` via dashes `─`
3. Each graph is self-contained — alignment does not need to match across different graphs
4. Pad with spaces to align vertical elements: repository names of different lengths need space padding before `│`

## Naming Conventions

### Step Names

Use snake_case descriptive names:
- `setup_seed`, `setup_execute` or `seed_create`, `seed_execute` — for initial stock seeding
- `create_<description>` — for movement creation steps
- `execute_<description>` — for execution steps
- `delete_<description>` — for deletion steps

### Test File Names

Use kebab-case with `.test.yaml` extension:
- `<feature-being-tested>.test.yaml`
- Group related tests by prefix (e.g., `collection-*` for collection movement tests)

### Item SKUs

Each file must use a unique SKU to avoid cross-test interference:
- Use descriptive names related to the test: `in-order-item`, `mixed-movement-item`

## Complete Example

```yaml
# Simple Item Movement
# Tests basic item movement from virtual to shelf with create and execute.
name: simple-item-movement
itemSKU: simple-item

# ========================================================================
# REPOSITORY STRUCTURE OVERVIEW
# ========================================================================
#
#  warehouse (root)
#  └─ shelf
#
# ========================================================================

repositories:
  # Virtual repository for initial stock additions
  - name: virtual
    parent: ""
    type: static
    virtual: true

  # Main warehouse container
  - name: warehouse
    parent: ""
    type: static
    virtual: false

  # Shelf - receives initial stock
  - name: shelf
    parent: warehouse
    type: static
    virtual: false

steps:
  # =========================================================================
  #  virtual                             warehouse
  #    └─[100 items]────────────────────► └─ shelf
  # =========================================================================
  - name: setup_seed
    action: create
    moveType: item
    from: virtual
    to: shelf
    qty: 100
    expectedStocks:
      virtual:   { qty:   0, ownQty:   0, in:   0, ownIn:   0, out: 100, ownOut: 100 }
      warehouse: { qty:   0, ownQty:   0, in: 100, ownIn:   0, out:   0, ownOut:   0 }
      shelf:     { qty:   0, ownQty:   0, in: 100, ownIn: 100, out:   0, ownOut:   0 }

  - name: setup_execute
    action: execute
    movement: setup_seed
    expectedStocks:
      virtual:   { qty:   0, ownQty:   0, in: 0, ownIn: 0, out: 0, ownOut: 0 }
      warehouse: { qty: 100, ownQty:   0, in: 0, ownIn: 0, out: 0, ownOut: 0 }
      shelf:     { qty: 100, ownQty: 100, in: 0, ownIn: 0, out: 0, ownOut: 0 }
```
