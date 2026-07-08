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
child_sum AS (
  SELECT r.parent_id AS repo, c.item_id, SUM(c.quantity) AS q
  FROM cur c
  JOIN %[1]s.repositories r ON r.id = c.repository_id
  WHERE r.parent_id IS NOT NULL
  GROUP BY 1, 2
),
node AS (
  SELECT repository_id AS repo, item_id FROM cur
  UNION
  SELECT repo, item_id FROM child_sum
)
SELECT
  n.repo::text,
  COALESCE(rp.name, '')                AS repo_name,
  n.item_id::text,
  COALESCE(cur.quantity, 0)            AS stored,
  COALESCE(cur.own_quantity, 0) + COALESCE(cs.q, 0) AS expected,
  COALESCE(cur.own_quantity, 0)           AS own_qty,
  COALESCE(cur.own_incoming_stock, 0)     AS own_incoming,
  COALESCE(cur.own_outgoing_stock, 0)     AS own_outgoing,
  COALESCE(cur.incoming_stock, 0)         AS incoming,
  COALESCE(cur.outgoing_stock, 0)         AS outgoing,
  COALESCE(cur.created_by::text, '')      AS created_by,
  (cur.repository_id IS NOT NULL)         AS has_current_row
FROM node n
LEFT JOIN cur         ON cur.repository_id = n.repo AND cur.item_id = n.item_id
LEFT JOIN child_sum cs ON cs.repo = n.repo AND cs.item_id = n.item_id
LEFT JOIN %[1]s.repositories rp ON rp.id = n.repo
WHERE COALESCE(cur.quantity, 0) <> COALESCE(cur.own_quantity, 0) + COALESCE(cs.q, 0)
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
child_sum AS (
  SELECT c.tenant_id, r.parent_id AS repo, c.item_id, SUM(c.quantity) AS q
  FROM cur c
  JOIN %[1]s.repositories r ON r.id = c.repository_id
  WHERE r.parent_id IS NOT NULL
  GROUP BY 1, 2, 3
),
node AS (
  SELECT tenant_id, repository_id AS repo, item_id FROM cur
  UNION
  SELECT tenant_id, repo, item_id FROM child_sum
)
SELECT
  n.tenant_id::text,
  n.repo::text,
  COALESCE(rp.name, '')                AS repo_name,
  n.item_id::text,
  COALESCE(cur.quantity, 0)            AS stored,
  COALESCE(cur.own_quantity, 0) + COALESCE(cs.q, 0) AS expected,
  COALESCE(cur.own_quantity, 0)           AS own_qty,
  COALESCE(cur.own_incoming_stock, 0)     AS own_incoming,
  COALESCE(cur.own_outgoing_stock, 0)     AS own_outgoing,
  COALESCE(cur.incoming_stock, 0)         AS incoming,
  COALESCE(cur.outgoing_stock, 0)         AS outgoing,
  COALESCE(cur.created_by::text, '')      AS created_by,
  (cur.repository_id IS NOT NULL)         AS has_current_row
FROM node n
LEFT JOIN cur ON cur.tenant_id = n.tenant_id AND cur.repository_id = n.repo AND cur.item_id = n.item_id
LEFT JOIN child_sum cs ON cs.tenant_id = n.tenant_id AND cs.repo = n.repo AND cs.item_id = n.item_id
LEFT JOIN %[1]s.repositories rp ON rp.id = n.repo
WHERE COALESCE(cur.quantity, 0) <> COALESCE(cur.own_quantity, 0) + COALESCE(cs.q, 0)
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

// queryMaxCreatedAt returns the max created_at for a tenant's stocks rows.
// Used by the quiescence check to detect in-flight writes.
// Args: $1 = tenant_id.
const queryMaxCreatedAt = `
SELECT COALESCE(MAX(created_at), '1970-01-01 00:00:00+00'::timestamptz)
FROM %[1]s.stocks
WHERE tenant_id = $1::uuid
`

// queryMaxVersion returns the max version for a (tenant, repo, item) triple,
// including soft-deleted rows (no deleted_at filter) to guarantee monotonicity.
// Args: $1 = tenant_id, $2 = repository_id, $3 = item_id.
const queryMaxVersion = `
SELECT COALESCE(MAX(version), 0)
FROM %[1]s.stocks
WHERE tenant_id = $1::uuid AND repository_id = $2::uuid AND item_id = $3::uuid
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
