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

// maxCreatedAt returns the maximum created_at across all stock rows for a tenant.
func maxCreatedAt(ctx context.Context, q querier, schema, tenantID string) (time.Time, error) {
	var t time.Time
	err := q.QueryRowContext(ctx, fmt.Sprintf(queryMaxCreatedAt, schema), tenantID).Scan(&t)
	return t, err
}

// maxVersion returns the highest version for a (tenant, repo, item) including soft-deleted rows.
func maxVersion(ctx context.Context, q querier, schema, tenantID, repoID, itemID string) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx, fmt.Sprintf(queryMaxVersion, schema), tenantID, repoID, itemID).Scan(&v)
	return v, err
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

// verifyAfterFix re-runs the rollup check inside the given querier and returns
// any violations whose (repo+"/"+item) key is in touched.
func verifyAfterFix(ctx context.Context, q querier, schema, tenantID string, touched map[string]bool) ([]RollupViolation, error) {
	all, err := rollupViolations(ctx, q, schema, tenantID)
	if err != nil {
		return nil, err
	}
	var remaining []RollupViolation
	for _, v := range all {
		if touched[v.RepoID+"/"+v.ItemID] {
			remaining = append(remaining, v)
		}
	}
	return remaining, nil
}
