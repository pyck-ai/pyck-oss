package dataindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/db"
	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// BackfillSQL returns the statement that fills a slot for rows written before the
// binding existed. The projection hook only fires on writes, so existing rows
// stay unindexed -- and a lookup would silently miss them -- until this runs.
//
// Chunked by id so a large table is never locked in one statement. Takes
// ($1 tenant_id, $2 data_type_slug, $3 afterID) -- tenant-scoped because one slug
// may carry different bindings per tenant.
//
// Both guards (slot IS NULL, the type check) are repeated in the outer UPDATE
// rather than left to the subquery, so at READ COMMITTED the UPDATE re-evaluates
// against the current row and cannot overwrite one the hook has since projected.
// That also makes the pass idempotent, and a row holding the wrong type is
// skipped rather than aborting the pass.
//
// FOR UPDATE SKIP LOCKED keeps the pass from blocking. A skipped row falls behind
// the id cursor and waits for the next boot, which starts from the beginning and
// still matches on IS NULL.
func BackfillSQL(schemaName, table string, b Binding, batchSize int) (string, error) {
	schema, err := quoteIdent(schemaName)
	if err != nil {
		return "", err
	}
	tbl, err := quoteIdent(table)
	if err != nil {
		return "", err
	}
	slot, err := quoteIdent(b.Slot)
	if err != nil {
		return "", err
	}
	path, err := sqlJSONPath(b.Source)
	if err != nil {
		return "", err
	}

	var expr, jsonType string
	switch b.Kind() {
	case mixin.SlotKindList:
		expr = backfillListExpr(path)
		jsonType = "array"
	case mixin.SlotKindText:
		expr = fmt.Sprintf("t.data #>> '%s'", path)
		jsonType = "string"
	case mixin.SlotKindNumeric:
		expr = fmt.Sprintf("(t.data #>> '%s')::double precision", path)
		jsonType = "number"
	case mixin.SlotKindBool:
		expr = fmt.Sprintf("(t.data #>> '%s')::boolean", path)
		jsonType = "boolean"
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownSlot, b.Slot)
	}

	// Reports the selected count as well as the written one: see Backfill's loop.
	return fmt.Sprintf(`WITH todo AS (
  SELECT id FROM %[1]s.%[2]s
  WHERE tenant_id = $1 AND data_type_slug = $2 AND id > $3
    AND %[3]s IS NULL AND jsonb_typeof(data #> '%[4]s') = '%[5]s'
  ORDER BY id LIMIT %[6]d
  FOR UPDATE SKIP LOCKED
), updated AS (
  UPDATE %[1]s.%[2]s AS t SET %[3]s = %[7]s
  FROM todo
  WHERE t.id = todo.id
    AND t.%[3]s IS NULL AND jsonb_typeof(t.data #> '%[4]s') = '%[5]s'
  RETURNING t.id
)
SELECT (SELECT count(*) FROM todo), (SELECT count(*) FROM updated), (SELECT id FROM todo ORDER BY id DESC LIMIT 1)`,
		schema, tbl, slot, path, jsonType, batchSize, expr), nil
}

// backfillListExpr rebuilds a list slot element by element so it matches what
// renderListElement writes on the live path -- otherwise the same payload would
// index differently depending on which path touched it. Empty strings and null
// drop; objects and arrays have no shared rendering and are skipped (the hook
// rejects them on a live write, but a pre-existing row must not abort the pass).
func backfillListExpr(path string) string {
	return fmt.Sprintf(`COALESCE((
    SELECT jsonb_agg(v ORDER BY ord)
    FROM (
      SELECT CASE jsonb_typeof(e)
               -- float8::text is the shortest round-trip Go's FormatFloat also
               -- produces; ::numeric::text then strips any exponent. Casting
               -- float8 straight to numeric rounds to 15 significant digits
               -- instead and diverges (1234567890123456 -> ...460, and
               -- 0.30000000000000004 -> 0.3).
               WHEN 'number'  THEN ((e#>>'{}')::float8::text)::numeric::text
               WHEN 'string'  THEN e#>>'{}'
               WHEN 'boolean' THEN e#>>'{}'
             END AS v, ord
      FROM jsonb_array_elements(t.data #> '%s') WITH ORDINALITY AS a(e, ord)
    ) elems
    WHERE v IS NOT NULL AND v <> ''
  ), '[]'::jsonb)`, path)
}

// sqlJSONPath turns a JSON pointer into the Postgres path array #> takes.
func sqlJSONPath(pointer string) (string, error) {
	parts, err := pointerSegments(pointer)
	if err != nil {
		return "", err
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// pointerSegments splits a JSON pointer into its resolved segments, rejecting any
// that would not survive the {a,b} path literal identically to how resolvePointer
// reads it. That shared rule is why the projection hook and the backfill index the
// same key: a comma splices one segment into two, whitespace is trimmed, and the
// quotes/braces/backslash break out of the literal.
func pointerSegments(pointer string) ([]string, error) {
	trimmed := strings.TrimPrefix(pointer, "/")
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty pointer", ErrBadSourcePath)
	}
	parts := strings.Split(trimmed, "/")
	for i, p := range parts {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		switch {
		case p == "":
			// "/a//b" and the "/a/" typo. Harmless-looking, but the path literal
			// becomes '{a,,b}', which Postgres rejects as a malformed array —
			// non-retryable, so it would fail the boot backfill rather than the
			// datatype save that introduced it.
			return nil, fmt.Errorf("%w: empty segment in %q", ErrBadSourcePath, pointer)
		case isAllDigits(p):
			// Ambiguous between an array index and a numeric object key, and the
			// two paths read it differently: '#>' descends into an array where
			// resolvePointer only walks objects. The row's slot would then flip
			// depending on which path wrote last.
			return nil, fmt.Errorf("%w: numeric segment %q", ErrBadSourcePath, p)
		case strings.ContainsAny(p, `"'{},\`) || strings.IndexFunc(p, isSpaceOrControl) >= 0:
			return nil, fmt.Errorf("%w: segment %q", ErrBadSourcePath, p)
		}
		parts[i] = p
	}
	return parts, nil
}

// isAllDigits reports whether s is a non-empty run of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isSpaceOrControl reports characters the path literal mishandles: whitespace is
// trimmed at a segment's edges, and control characters are ambiguous.
func isSpaceOrControl(r rune) bool {
	return r <= ' ' || r == 0x7f
}

// Querier is the part of *sql.DB the backfill needs.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Backfill walks every binding of a datatype in batches until no rows are left.
// It is safe to run on every boot: a second pass matches nothing.
func Backfill(ctx context.Context, q Querier, schemaName, table string, tenantID uuid.UUID, dataTypeSlug string, bindings Bindings, batchSize int) (int, error) {
	total := 0
	for _, b := range bindings {
		stmt, err := BackfillSQL(schemaName, table, b, batchSize)
		if err != nil {
			return total, fmt.Errorf("index %q: %w", b.Name, err)
		}
		lastID := uuid.Nil
		for {
			res, err := backfillBatch(ctx, q, stmt, tenantID, dataTypeSlug, lastID)
			if err != nil {
				return total, fmt.Errorf("index %q: %w", b.Name, err)
			}
			// Stop on what the batch selected, not on what it wrote: a batch whose
			// rows were all locked elsewhere or already projected writes nothing
			// while rows remain beyond it.
			if res.selected == 0 {
				break
			}
			total += res.updated
			lastID = res.lastID
		}
	}
	return total, nil
}

// backfillBatch runs one chunk, retrying a serialization or deadlock failure a
// bounded number of times: the writer runs at SERIALIZABLE, so a concurrent write
// can abort the batch, and a correctness backfill must not tolerate that silently.
// A persistent or non-retryable failure is returned so the caller can fail boot.
func backfillBatch(ctx context.Context, q Querier, stmt string, tenantID uuid.UUID, dataTypeSlug string, afterID uuid.UUID) (batchResult, error) {
	const maxAttempts = 5
	var lastErr error
	for attempt := range maxAttempts {
		res, err := runBackfillBatch(ctx, q, stmt, tenantID, dataTypeSlug, afterID)
		if err == nil {
			return res, nil
		}
		if !db.ErrIsRetryable(err) {
			return batchResult{}, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return batchResult{}, ctx.Err()
		case <-time.After(db.GetSleepDuration(attempt + 1)):
		}
	}
	return batchResult{}, fmt.Errorf("backfill batch still failing after %d attempts: %w", maxAttempts, lastErr)
}

// batchResult is one chunk's outcome: how many rows the cursor passed over,
// how many were actually projected, and where to resume.
type batchResult struct {
	selected int
	updated  int
	lastID   uuid.UUID
}

// runBackfillBatch runs one chunk and reports what it selected, what it wrote,
// and the id to resume after.
func runBackfillBatch(ctx context.Context, q Querier, stmt string, tenantID uuid.UUID, dataTypeSlug string, afterID uuid.UUID) (res batchResult, err error) {
	rows, err := q.QueryContext(ctx, stmt, tenantID, dataTypeSlug, afterID)
	if err != nil {
		return batchResult{}, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("backfill batch: close rows: %w", cerr)
		}
	}()

	if !rows.Next() {
		if rerr := rows.Err(); rerr != nil {
			return batchResult{}, rerr
		}
		return batchResult{}, nil
	}
	// The cursor is NULL when the batch selected nothing (there is no max(uuid)
	// aggregate), so it scans as a nullable value.
	var lastID uuid.NullUUID
	if serr := rows.Scan(&res.selected, &res.updated, &lastID); serr != nil {
		return batchResult{}, serr
	}
	if rerr := rows.Err(); rerr != nil {
		return batchResult{}, rerr
	}
	res.lastID = lastID.UUID
	return res, nil
}
