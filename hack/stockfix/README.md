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

Repairs `quantity` on violating current stock rows by appending one corrective
row per violating `(repo, item)`. The corrective row is a copy of the live
current row with:

- `quantity` set to `own_quantity + SUM(direct children current quantity)`
- `version` set to `MAX(version including soft-deleted) + 1`
- `movement_id` set to **NULL** (marks the row as a manual correction)
- `created_at` set to `NOW()`
- all other fields copied from the live current row

All inserts are wrapped in a single transaction. Before committing, the
invariant check is re-run inside the transaction; if any touched pair is still
violated the transaction is rolled back.

**Caveats**

- `movement_id = NULL` rows are excluded when `RebuildStockTable` replays the
  ledger. A future rebuild will lose these corrections. **Permanent correctness
  requires the write-path hotfix** (deployed in the same release). These rows
  are for unblocking operations only.
- Only `quantity` is repaired. `incoming_stock`, `outgoing_stock`,
  `own_incoming_stock`, `own_outgoing_stock` are never auto-modified.

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

Before mutating, the tool reads `MAX(created_at)` from the tenant's stock rows
twice (separated by `--settle`, default 20 s). If the value changes between
reads, concurrent writes are in flight and the tool aborts.

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
| Quiescence | Two reads of `MAX(created_at)` with `--settle` gap; aborts if changed |
| Transaction | All inserts in one transaction; rolled back if post-insert verify fails |
| Workers | Stopped before mutating (via `--fly-app`/`--k8s-deploy`), restarted after commit |
| `movement_id` | Always NULL on corrective rows — distinguishes them from ledger-driven rows |
| Exit codes | 0 = clean / success, 1 = error, 3 = violations found (script-friendly) |

---

## Building

```bash
task build        # → bin/stockfix
task test         # unit tests (no DB required)
task lint         # go vet
task tidy         # go mod tidy
```

Module: `github.com/pyck-ai/pyck/hack/stockfix` (standalone; not in go.work).
