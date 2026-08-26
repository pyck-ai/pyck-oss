package main

// All SQL lives here for reviewability.
//
// Every query uses fmt.Sprintf(query, schema) before execution — the schema
// name is substituted into %[1]s placeholders. Schema is validated against
// [a-z0-9_] before use (see validateSchema in db.go), so this is not a SQL
// injection risk. Positional parameters ($1, $2, …) handle all user-supplied
// values.

// queryRollupByTenant finds (repo, item) pairs where the current stock row's
// quantity ≠ own_quantity + sum(direct children's current quantity).
// Returns one row per violating pair with all fields needed to build a corrective row.
//
// Only live repositories take part: create_item_movement_proc's ancestor walk
// skips soft-deleted repos, so counting their frozen stock would invent a
// violation on the parent that no write path can produce or resolve.
// Args: $1 = tenant_id.
const queryRollupByTenant = `
WITH cur AS (
  SELECT DISTINCT ON (repository_id, item_id)
    repository_id, item_id, quantity, own_quantity,
    own_incoming_stock, own_outgoing_stock, incoming_stock, outgoing_stock, created_by
  FROM %[1]s.stocks
  WHERE tenant_id = $1::uuid AND deleted_at IS NULL
  ORDER BY repository_id, item_id, version DESC
),
live AS (
  SELECT c.*, r.parent_id
  FROM cur c
  JOIN %[1]s.repositories r ON r.id = c.repository_id AND r.deleted_at IS NULL
),
child_sum AS (
  SELECT parent_id AS repo, item_id, SUM(quantity) AS q
  FROM live
  WHERE parent_id IS NOT NULL
  GROUP BY 1, 2
),
node AS (
  SELECT repository_id AS repo, item_id FROM live
  UNION
  SELECT repo, item_id FROM child_sum
)
SELECT
  n.repo::text,
  COALESCE(rp.name, '')                AS repo_name,
  n.item_id::text,
  COALESCE(lv.quantity, 0)             AS stored,
  COALESCE(lv.own_quantity, 0) + COALESCE(cs.q, 0) AS expected,
  COALESCE(lv.own_quantity, 0)            AS own_qty,
  COALESCE(lv.own_incoming_stock, 0)      AS own_incoming,
  COALESCE(lv.own_outgoing_stock, 0)      AS own_outgoing,
  COALESCE(lv.incoming_stock, 0)          AS incoming,
  COALESCE(lv.outgoing_stock, 0)          AS outgoing,
  COALESCE(lv.created_by::text, '')       AS created_by,
  (lv.repository_id IS NOT NULL)          AS has_current_row
FROM node n
JOIN %[1]s.repositories rp ON rp.id = n.repo AND rp.deleted_at IS NULL
LEFT JOIN live lv      ON lv.repository_id = n.repo AND lv.item_id = n.item_id
LEFT JOIN child_sum cs ON cs.repo = n.repo AND cs.item_id = n.item_id
WHERE COALESCE(lv.quantity, 0) <> COALESCE(lv.own_quantity, 0) + COALESCE(cs.q, 0)
ORDER BY n.repo, n.item_id
`

// queryRollupAllTenants is queryRollupByTenant without a tenant filter.
// Returns tenant_id as the first column.
// No args.
const queryRollupAllTenants = `
WITH cur AS (
  SELECT DISTINCT ON (tenant_id, repository_id, item_id)
    tenant_id, repository_id, item_id, quantity, own_quantity,
    own_incoming_stock, own_outgoing_stock, incoming_stock, outgoing_stock, created_by
  FROM %[1]s.stocks
  WHERE deleted_at IS NULL
  ORDER BY tenant_id, repository_id, item_id, version DESC
),
live AS (
  SELECT c.*, r.parent_id
  FROM cur c
  JOIN %[1]s.repositories r ON r.id = c.repository_id AND r.deleted_at IS NULL
),
child_sum AS (
  SELECT tenant_id, parent_id AS repo, item_id, SUM(quantity) AS q
  FROM live
  WHERE parent_id IS NOT NULL
  GROUP BY 1, 2, 3
),
node AS (
  SELECT tenant_id, repository_id AS repo, item_id FROM live
  UNION
  SELECT tenant_id, repo, item_id FROM child_sum
)
SELECT
  n.tenant_id::text,
  n.repo::text,
  COALESCE(rp.name, '')                AS repo_name,
  n.item_id::text,
  COALESCE(lv.quantity, 0)             AS stored,
  COALESCE(lv.own_quantity, 0) + COALESCE(cs.q, 0) AS expected,
  COALESCE(lv.own_quantity, 0)            AS own_qty,
  COALESCE(lv.own_incoming_stock, 0)      AS own_incoming,
  COALESCE(lv.own_outgoing_stock, 0)      AS own_outgoing,
  COALESCE(lv.incoming_stock, 0)          AS incoming,
  COALESCE(lv.outgoing_stock, 0)          AS outgoing,
  COALESCE(lv.created_by::text, '')       AS created_by,
  (lv.repository_id IS NOT NULL)          AS has_current_row
FROM node n
JOIN %[1]s.repositories rp ON rp.id = n.repo AND rp.deleted_at IS NULL
LEFT JOIN live lv      ON lv.tenant_id = n.tenant_id AND lv.repository_id = n.repo AND lv.item_id = n.item_id
LEFT JOIN child_sum cs ON cs.tenant_id = n.tenant_id AND cs.repo = n.repo AND cs.item_id = n.item_id
WHERE COALESCE(lv.quantity, 0) <> COALESCE(lv.own_quantity, 0) + COALESCE(cs.q, 0)
ORDER BY n.tenant_id, n.repo, n.item_id
`

// queryLedgerAnomalies finds stock rows whose quantity/incoming/outgoing deltas
// do not match any of the four legal movement patterns. Rows are joined to
// item_movements and repository_movements to classify movement kind and timing.
// Args: $1 = repository_id, $2 = item_id, $3 = tenant_id, $4 = since (timestamptz).
const queryLedgerAnomalies = `
WITH led AS (
  SELECT
    version,
    quantity          AS q,
    incoming_stock    AS inc,
    outgoing_stock    AS outg,
    created_at,
    movement_id,
    LAG(quantity)       OVER w AS pq,
    LAG(incoming_stock) OVER w AS pinc,
    LAG(outgoing_stock) OVER w AS poutg
  FROM %[1]s.stocks
  WHERE repository_id = $1::uuid AND item_id = $2::uuid AND tenant_id = $3::uuid
  WINDOW w AS (ORDER BY version)
)
SELECT
  led.version,
  COALESCE(led.pq, 0)              AS prev_qty,
  led.q                            AS qty,
  (led.q    - led.pq)              AS dq,
  (led.inc  - led.pinc)            AS dinc,
  (led.outg - led.poutg)           AS doutg,
  led.created_at,
  led.movement_id::text,
  CASE
    WHEN im.id IS NOT NULL THEN 'item'
    WHEN rm.id IS NOT NULL THEN 'repo'
    ELSE ''
  END                              AS mov_kind,
  im.created_at                    AS im_created_at,
  im.executed_at                   AS im_executed_at,
  rm.created_at                    AS rm_created_at,
  rm.executed_at                   AS rm_executed_at
FROM led
LEFT JOIN %[1]s.item_movements       im ON im.id = led.movement_id
LEFT JOIN %[1]s.repository_movements rm ON rm.id = led.movement_id
WHERE led.pq IS NOT NULL
  AND led.created_at >= $4
  AND NOT (
      (led.q - led.pq) = 0
   OR ((led.q - led.pq) > 0  AND (led.inc  - led.pinc)  = -(led.q - led.pq))
   OR ((led.q - led.pq) < 0  AND (led.outg - led.poutg) =   (led.q - led.pq))
   OR ((led.inc - led.pinc) = 0 AND (led.outg - led.poutg) = 0 AND (led.q - led.pq) <> 0)
  )
ORDER BY led.version
`

// queryQuiescence fingerprints a tenant's stocks table for the quiescence check.
//
// Deliberately NOT max(created_at): created_at is the writing pod's wall clock,
// so a straggler whose clock lags inserts below the current max and the value
// never moves — the guard would report "stable" while writes land. stocks is
// append-only, so row count plus the version sum only ever grow.
// Args: $1 = tenant_id.
// The cast pins the scan target: SUM over bigint yields numeric.
const queryQuiescence = `
SELECT COUNT(*), COALESCE(SUM(version), 0)::bigint
FROM %[1]s.stocks
WHERE tenant_id = $1::uuid
`

// queryMaxVersions returns the max version per (repo, item) for a tenant,
// including soft-deleted rows (no deleted_at filter) so an assigned version
// clears the whole unique index.
// Args: $1 = tenant_id.
const queryMaxVersions = `
SELECT repository_id::text, item_id::text, MAX(version)
FROM %[1]s.stocks
WHERE tenant_id = $1::uuid
GROUP BY 1, 2
`

// queryRepoParents returns parent_id per live repository ("" for a root), which
// is how the repair orders children before parents.
// Args: $1 = tenant_id.
const queryRepoParents = `
SELECT id::text, COALESCE(parent_id::text, '')
FROM %[1]s.repositories
WHERE tenant_id = $1::uuid AND deleted_at IS NULL
`

// queryInsertCorrective appends one corrective stock row.
// movement_id is intentionally NULL — it marks the row as a manual correction.
// A future RebuildStockTable will replay the ledger without this row; the write-path
// hotfix is required for permanent correctness.
// Args: $1=id $2=tenant_id $3=repository_id $4=item_id $5=version
//
//	$6=quantity $7=own_quantity $8=incoming_stock $9=outgoing_stock
//	$10=own_incoming_stock $11=own_outgoing_stock $12=created_by.
const queryInsertCorrective = `
INSERT INTO %[1]s.stocks (
  id, tenant_id, repository_id, item_id, version,
  quantity, own_quantity, incoming_stock, outgoing_stock,
  own_incoming_stock, own_outgoing_stock,
  movement_id, created_by, created_at
) VALUES (
  $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
  $6, $7, $8, $9,
  $10, $11,
  NULL, $12::uuid, NOW()
)
`
