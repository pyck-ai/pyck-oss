package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// querier is satisfied by both *sql.DB and *sql.Tx, enabling transactional fix logic.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// openDB opens and pings a PostgreSQL connection.
func openDB(ctx context.Context, url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}

// validateSchema rejects schema names that contain characters outside [a-z0-9_].
// Schema names are interpolated into SQL via fmt.Sprintf, so this is the injection guard.
func validateSchema(s string) error {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return fmt.Errorf("schema %q contains invalid character %q (only a-z, 0-9, _ allowed)", s, r)
		}
	}
	if s == "" {
		return fmt.Errorf("schema must not be empty")
	}
	return nil
}

// rollupViolations returns (repo, item) pairs whose current stock quantity
// does not equal own_quantity + sum(direct children current quantity).
// If tenantID is empty, all tenants are scanned.
func rollupViolations(ctx context.Context, q querier, schema, tenantID string) ([]RollupViolation, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if tenantID == "" {
		rows, err = q.QueryContext(ctx, fmt.Sprintf(queryRollupAllTenants, schema))
	} else {
		rows, err = q.QueryContext(ctx, fmt.Sprintf(queryRollupByTenant, schema), tenantID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RollupViolation
	for rows.Next() {
		var v RollupViolation
		if tenantID == "" {
			if err := rows.Scan(
				&v.TenantID, &v.RepoID, &v.RepoName, &v.ItemID,
				&v.StoredQty, &v.ExpectedQty,
				&v.OwnQty, &v.OwnIncomingQty, &v.OwnOutgoingQty,
				&v.IncomingQty, &v.OutgoingQty,
				&v.CreatedBy, &v.HasCurrentRow,
			); err != nil {
				return nil, err
			}
		} else {
			v.TenantID = tenantID
			if err := rows.Scan(
				&v.RepoID, &v.RepoName, &v.ItemID,
				&v.StoredQty, &v.ExpectedQty,
				&v.OwnQty, &v.OwnIncomingQty, &v.OwnOutgoingQty,
				&v.IncomingQty, &v.OutgoingQty,
				&v.CreatedBy, &v.HasCurrentRow,
			); err != nil {
				return nil, err
			}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ledgerAnomalies returns stock rows for (tenant, repo, item) whose quantity/
// incoming/outgoing deltas do not match any of the four legal movement patterns.
func ledgerAnomalies(ctx context.Context, q querier, schema, tenantID, repoID, itemID string, since time.Time) ([]AnomalyRow, error) {
	rows, err := q.QueryContext(ctx,
		fmt.Sprintf(queryLedgerAnomalies, schema),
		repoID, itemID, tenantID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AnomalyRow
	for rows.Next() {
		var a AnomalyRow
		var movID sql.NullString
		var imCreated, imExecuted, rmCreated, rmExecuted sql.NullTime
		if err := rows.Scan(
			&a.Version, &a.PrevQty, &a.Qty, &a.DQ, &a.DINC, &a.DOUTG,
			&a.CreatedAt, &movID, &a.MovKind,
			&imCreated, &imExecuted, &rmCreated, &rmExecuted,
		); err != nil {
			return nil, err
		}
		if movID.Valid {
			a.MovementID = movID.String
		}
		switch {
		case imCreated.Valid:
			a.MovCreatedAt = &imCreated.Time
		case rmCreated.Valid:
			a.MovCreatedAt = &rmCreated.Time
		}
		switch {
		case imExecuted.Valid:
			a.MovExecutedAt = &imExecuted.Time
		case rmExecuted.Valid:
			a.MovExecutedAt = &rmExecuted.Time
		}
		a.Class = classifyDelta(a.DQ, a.DINC, a.DOUTG, a.MovKind)
		out = append(out, a)
	}
	return out, rows.Err()
}

// quiescenceMark is the append-only fingerprint of a tenant's stocks table:
// both fields only grow, so any write between two reads moves at least one.
type quiescenceMark struct {
	Rows     int64
	VersionS int64
}

func (m quiescenceMark) String() string {
	return fmt.Sprintf("rows=%d versionSum=%d", m.Rows, m.VersionS)
}

// quiescence reads the fingerprint used to detect in-flight writes.
func quiescence(ctx context.Context, q querier, schema, tenantID string) (quiescenceMark, error) {
	var m quiescenceMark
	err := q.QueryRowContext(ctx, fmt.Sprintf(queryQuiescence, schema), tenantID).Scan(&m.Rows, &m.VersionS)
	return m, err
}

// maxVersions returns the highest version per (repo, item) for a tenant,
// including soft-deleted rows. One query covers every pair the repair touches.
func maxVersions(ctx context.Context, q querier, schema, tenantID string) (map[stockPair]int64, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf(queryMaxVersions, schema), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[stockPair]int64{}
	for rows.Next() {
		var p stockPair
		var v int64
		if err := rows.Scan(&p.RepoID, &p.ItemID, &v); err != nil {
			return nil, err
		}
		out[p] = v
	}
	return out, rows.Err()
}

// repoParents returns parent_id per live repository ("" for a root).
func repoParents(ctx context.Context, q querier, schema, tenantID string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf(queryRepoParents, schema), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var id, parent string
		if err := rows.Scan(&id, &parent); err != nil {
			return nil, err
		}
		out[id] = parent
	}
	return out, rows.Err()
}

// insertCorrectiveRow inserts one corrective stock row within the supplied querier (typically a *sql.Tx).
func insertCorrectiveRow(ctx context.Context, q querier, schema string, r CorrectiveRow) error {
	_, err := q.ExecContext(ctx, fmt.Sprintf(queryInsertCorrective, schema),
		r.ID, r.TenantID, r.RepoID, r.ItemID, r.Version,
		r.Quantity, r.OwnQuantity, r.Incoming, r.Outgoing,
		r.OwnIncoming, r.OwnOutgoing,
		r.CreatedBy,
	)
	return err
}

// verifyAfterFix re-runs the rollup check and returns every violation the
// repair is answerable for: the whole tenant, minus what was already unfixable.
//
// Scoping it to the pairs the repair touched would miss its own fallout — a
// parent that was consistent can come out violated — and commit the shifted
// drift while reporting success.
func verifyAfterFix(ctx context.Context, q querier, schema, tenantID string, preexisting map[stockPair]bool) ([]RollupViolation, error) {
	all, err := rollupViolations(ctx, q, schema, tenantID)
	if err != nil {
		return nil, err
	}
	var remaining []RollupViolation
	for _, v := range all {
		if !preexisting[v.Pair()] {
			remaining = append(remaining, v)
		}
	}
	return remaining, nil
}
