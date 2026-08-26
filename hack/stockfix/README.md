# stockfix

Operator tool to detect and repair production stock-ledger corruption in the
`inventory` service.

## Background

`inventory.stocks` is an append-only table keyed by `(tenant_id, repository_id, item_id, version)`.
The "current" row for each pair is the one with the highest `version`. A write-path
bug (two-snapshot fan-out in repository-movement create/delete) could clobber
concurrent commits, leaving parent-rollup rows whose `quantity` disagrees with
`own_quantity + SUM(direct children's current quantity)`.

---

## Quick start

```
# Read-only analysis of all tenants (always safe, exit 3 if violations found)
stockfix analyze --db-url postgres://...

# Restrict to one tenant
stockfix analyze --db-url postgres://... --tenant <uuid>

# Ledger anomaly scan for a specific (repo, item)
stockfix analyze --db-url postgres://... --tenant <uuid> \
  --repo <repo-uuid> --item <item-uuid> --since 7d

# Dry-run fix (no mutations)
stockfix fix --db-url postgres://... --tenant <uuid>

# Execute fix (Hellmann Fly app)
stockfix fix --db-url postgres://... --tenant <uuid> --execute \
  --fly-app pyck-hellmann-voelklingen

# Execute fix (generic K8s)
stockfix fix --db-url postgres://... --tenant <uuid> --execute \
  --k8s-deploy prod/inventory-worker
```

---

## DB URL precedence

`--db-url` flag > `PYCK_DATABASE_URL` env > `PYCK_DATABASE_MASTER_URL` env

---

## Subcommands

### `analyze` — read-only (always safe)

Runs two checks and exits **0** if clean, **3** if any violation or anomaly is found.

**Check 1 — rollup invariant**

Every `(repo, item)` where the current stock row's `quantity` ≠
`own_quantity + SUM(direct children's current quantity)`.

Example output:

```
=== ROLLUP INVARIANT CHECK ===
  FAIL — 2 violation(s) found:

TENANT                                REPO_ID                               REPO_NAME  ITEM_ID                               STORED  EXPECTED  DIFF  HAS_ROW
------                                -------                                ---------  -------                               ------  --------  ----  -------
550e8400-…                            a3bb1897-…                            Shelf A    c1d2e3f4-…                            90      100       +10   true
550e8400-…                            b4cc2908-…                            Aisle 3    d2e3f4a5-…                            200     150       -50   true
```

**Check 2 — ledger anomaly scan**

For each invariant violator (or an explicit `--repo`/`--item`), scans rows in
the `--since` window (default 7 days) whose quantity/incoming/outgoing deltas
do not match any of the four legal movement patterns:

| Pattern | Condition |
|---|---|
| Pure mark | `dq = 0` |
| Incoming execute | `dq > 0 AND dinc = -dq` |
| Outgoing execute | `dq < 0 AND doutg = dq` |
| Bare repo-entry/exit | `dq ≠ 0 AND dinc = 0 AND doutg = 0` |

Rows matching `dq < 0 AND doutg < 0 AND dinc = 0` are classified
`LEGAL-FLAGGED` (combined repo-exit + mark-release; verify against movement type).

A row on an `item_movement` that changes quantity is classified `CLOBBER` —
the definitive fingerprint of the write-path bug.

**Flags**

| Flag | Default | Description |
|---|---|---|
| `--db-url` | — | PostgreSQL URL |
| `--tenant` | all | Tenant UUID |
| `--schema` | `inventory` | Schema name |
| `--repo` | — | Repository UUID (requires `--item`) |
| `--item` | — | Item UUID (requires `--repo`) |
| `--since` | `7d` | Anomaly scan window (e.g. `7d`, `168h`) |
| `--json` | false | Emit JSON instead of tables |

---

### `fix` — repair rollup quantity (dry-run by default)

Repairs `quantity` by appending one corrective row per `(repo, item)` it has to
correct. The corrective row is a copy of the live current row with:

- `quantity` set to `own_quantity + SUM(direct children current quantity)`
- `version` set to `MAX(version including soft-deleted) + 1`
- `movement_id` set to **NULL** (marks the row as a manual correction)
- `created_at` set to `NOW()`
- all other fields copied from the live current row

Everything happens in one transaction, children before parents: a parent is
priced only once every violation below it has been corrected, so it is never
written against a child value that is about to move. Before committing, the
invariant is re-checked across the **whole tenant** — not just the pairs that
were touched — and anything still violated that was not already unfixable rolls
the transaction back.

**Caveats**

- `movement_id = NULL` rows are excluded when `RebuildStockTable` replays the
  ledger. A future rebuild will lose these corrections. **Permanent correctness
  requires the write-path hotfix** (deployed in the same release). These rows
  are for unblocking operations only.
- Only `quantity` is repaired. `incoming_stock`, `outgoing_stock`,
  `own_incoming_stock`, `own_outgoing_stock` are never auto-modified.
- Corrections run children before parents, and a parent is repriced whenever a
  child's correction moves its expected sum — so a single run can write rows for
  pairs that were consistent when it started. The plan lists every one of them.
- Soft-deleted repositories take no part: `create_item_movement_proc`'s ancestor
  walk skips them, so their stock is frozen and never counts towards a parent.
- A dry run is the same repair rolled back, so it holds locks on the version
  index entries it writes — the same ones a concurrent movement would take. It
  blocks real writes while it runs, and aborts after 60 s to bound that. Prefer
  off-peak, or point `--db-url` at a replica.

**Flags**

| Flag | Default | Description |
|---|---|---|
| `--db-url` | — | PostgreSQL URL |
| `--tenant` | — | **Required** — tenant UUID |
| `--schema` | `inventory` | Schema name |
| `--execute` | false | Actually perform mutations |
| `--dry-run` | false | Explicit no-op confirmer |
| `--yes` | false | Skip interactive tenant-ID confirm prompt |
| `--settle` | `20s` | Quiescence settle interval |
| `--assume-quiesced` | false | Skip quiescence check (dangerous) |
| `--fly-app` | — | Fly.io app name for automatic worker stop/start |
| `--k8s-deploy` | — | K8s `ns/deploy` for automatic worker stop/start |

**Quiescence check**

Before mutating, the tool reads `COUNT(*)` and `SUM(version)` over the tenant's
stock rows twice (separated by `--settle`, default 20 s). If either moves,
concurrent writes are in flight and the tool aborts. Both only ever grow on an
append-only table, so a straggler pod whose clock lags cannot slip a write past
the guard the way it could past a `MAX(created_at)` comparison.

`--execute` without `--assume-quiesced` requires either `--fly-app` or
`--k8s-deploy` so workers are stopped first. Use `--assume-quiesced` only when
you can guarantee no concurrent writes (e.g. off-hours maintenance window).

---

### `workers` — manage inventory workers

```
stockfix workers stop|start|status --fly-app <name>
stockfix workers stop|start|status --k8s-deploy <ns/deploy>
```

Stopped state is persisted in `.stockfix-workers.json` in the current directory
so `start` can restore the original replica count / machine IDs.

**Actions**

| Action | Fly | K8s |
|---|---|---|
| `stop` | `flyctl machine stop <id> --app <app>` for each started machine | `kubectl scale deploy/<d> -n <ns> --replicas=0` |
| `start` | `flyctl machine start <id> --app <app>` for each previously stopped machine | `kubectl scale deploy/<d> -n <ns> --replicas=<saved>` |
| `status` | `flyctl machine list --app <app>` | `kubectl get deploy/<d> -n <ns>` |

Add `--dry-run` to print commands without executing.

**Hellmann (Fly.io — app `pyck-hellmann-voelklingen`)**

```bash
# Stop before fix
stockfix workers stop --fly-app pyck-hellmann-voelklingen

# Check status
stockfix workers status --fly-app pyck-hellmann-voelklingen

# Start after fix
stockfix workers start --fly-app pyck-hellmann-voelklingen
```

Or let `fix --execute` handle the full flow automatically:

```bash
stockfix fix \
  --db-url "$PYCK_DATABASE_MASTER_URL" \
  --tenant 550e8400-e29b-41d4-a716-446655440000 \
  --execute \
  --fly-app pyck-hellmann-voelklingen
```

**Generic K8s worker**

```bash
stockfix fix \
  --db-url "$PYCK_DATABASE_MASTER_URL" \
  --tenant 550e8400-e29b-41d4-a716-446655440000 \
  --execute \
  --k8s-deploy prod/inventory-worker
```

---

## SAFETY

| Property | Behaviour |
|---|---|
| Default mode | **Dry-run** — connects to DB read-only, prints plan, exits 0 |
| Mutations | Only with explicit `--execute` |
| Interactive confirm | Operator must re-type the tenant UUID before any INSERT (bypass with `--yes`) |
| Quiescence | Two reads of `COUNT(*)` + `SUM(version)` with `--settle` gap; aborts if changed |
| Transaction | All inserts in one transaction; rolled back if post-insert verify fails |
| Workers | Stopped before mutating (via `--fly-app`/`--k8s-deploy`), restarted after commit |
| `movement_id` | Always NULL on corrective rows — distinguishes them from ledger-driven rows |
| Exit codes | 0 = clean / success, 1 = error, 3 = violations found (script-friendly) |

---

## Building

```bash
task build        # → bin/stockfix
task test         # unit tests; the DB-backed ones skip without a database
task lint         # go vet
task tidy         # go mod tidy

# The rollup invariant and the repair loop, against a real Postgres. Each test
# creates and drops its own schema, so any database you can CREATE SCHEMA in
# works — no pyck schema, no migrations.
task test:db DB_URL='postgres://admin:12345@localhost:5432/pyck_dev?sslmode=disable'
```

Module: `github.com/pyck-ai/pyck/hack/stockfix` (standalone; not in go.work).

## CI

`run-tests` gives this module its own job with a Postgres service, so the
database-backed tests run instead of skipping. Separate rather than folded into
the backend matrix: that matrix has no use for the service, and testcontainers
would cost the module its pgx-and-testify dependency set — the property that
makes it easy to build and copy to a jump host.

The repo's golangci-lint set does not run here: it targets services and flags a
print-heavy CLI on nearly every line (unchecked `fmt.Fprintln` returns,
`flag.Parse`, missing `t.Parallel`). `task lint` is `go vet`.
