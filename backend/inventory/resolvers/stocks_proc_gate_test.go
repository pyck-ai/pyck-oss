package resolvers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
)

// TestCreateItemMovementProc_GateReadsHighestVersion_Postgres pins the §2
// availability gate of inventory.create_item_movement_proc on version.
//
// The gate used to read the FROM row by created_at, which is the writing pod's
// wall clock and not a total order: under skew it could pick a superseded row
// and reject a valid move as STOCK_INSUFFICIENT (or, with the quantities the
// other way round, admit an over-move). The Go-side selectors are pinned by
// TestCreateItemMovementViaGo_GateReadsHighestVersion; this covers the proc,
// which is the path production actually takes on Postgres.
func TestCreateItemMovementProc_GateReadsHighestVersion_Postgres(t *testing.T) {
	t.Parallel()

	env, _, testDSN := setupPostgresWithGate(t)
	apiClient := setupAPIClient(t, env)
	ctx := env.ctx(userA)

	virtualID := stockTestCreateRepository(t, ctx, apiClient, "gate-virtual", entrepository.TypeStatic, true, nil)
	warehouseID := stockTestCreateRepository(t, ctx, apiClient, "gate-warehouse", entrepository.TypeStatic, false, nil)
	slotID := stockTestCreateRepository(t, ctx, apiClient, "gate-slot", entrepository.TypeStatic, false, &warehouseID)
	boxID := stockTestCreateRepository(t, ctx, apiClient, "gate-box", entrepository.TypeStatic, false, &warehouseID)
	itemID := stockTestCreateItem(t, ctx, apiClient, "gate-proc-item")

	// Place 10 units at the slot. That leaves two rows: v0 from CREATE
	// (quantity 0, the baseline at that instant) and v1 from EXECUTE
	// (quantity 10).
	placeMvID := stockTestCreateItemMovement(t, ctx, apiClient, itemID, virtualID, slotID, 10)
	stockTestExecuteItemMovement(t, ctx, apiClient, placeMvID)

	tenantID := userA.TenantID
	createdBy := userA.ID

	// Invert the clock against the versions: the superseded v0 row — the one
	// carrying quantity 0 — is given the LATEST created_at, exactly what a pod
	// whose clock runs ahead produces. Ordering by created_at now yields "0
	// available"; ordering by version still yields 10.
	conn, err := pgx.Connect(ctx, testDSN)
	require.NoError(t, err)
	defer func() {
		if cerr := conn.Close(ctx); cerr != nil {
			t.Logf("closing probe connection: %v", cerr)
		}
	}()

	// The reservation columns go with it: the gate reads availability as
	// quantity + incoming − outgoing, and the CREATE row still carries the
	// placement's +10 incoming, which would read as 10 available on its own.
	tag, err := conn.Exec(ctx, `
		UPDATE inventory.stocks
		SET created_at = (
			SELECT MAX(created_at) + interval '1 hour'
			FROM inventory.stocks
			WHERE tenant_id = $1 AND repository_id = $2 AND item_id = $3
		),
		incoming_stock = 0, own_incoming_stock = 0
		WHERE tenant_id = $1 AND repository_id = $2 AND item_id = $3 AND version = 0`,
		tenantID, uuid.MustParse(slotID), uuid.MustParse(itemID))
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected(), "expected the v0 row to re-stamp")

	// Sanity: the fixture really does disagree between the two orderings, on
	// the same expression the gate evaluates.
	const availability = `
		SELECT COALESCE(quantity, 0) + COALESCE(incoming_stock, 0) - COALESCE(outgoing_stock, 0)
		FROM inventory.stocks
		WHERE tenant_id = $1 AND repository_id = $2 AND item_id = $3 AND deleted_at IS NULL
		ORDER BY %s LIMIT 1`

	var byCreatedAt, byVersion int64
	require.NoError(t, conn.QueryRow(ctx,
		fmt.Sprintf(availability, "created_at DESC, version DESC"),
		tenantID, uuid.MustParse(slotID), uuid.MustParse(itemID)).Scan(&byCreatedAt))
	require.NoError(t, conn.QueryRow(ctx,
		fmt.Sprintf(availability, "version DESC"),
		tenantID, uuid.MustParse(slotID), uuid.MustParse(itemID)).Scan(&byVersion))
	require.EqualValues(t, 0, byCreatedAt, "the skewed fixture must read as empty by created_at")
	require.EqualValues(t, 10, byVersion, "and as stocked by version")

	// The move is valid: 10 are available at the slot. A gate reading the
	// superseded row raises STOCK_INSUFFICIENT here.
	var movementID uuid.UUID
	err = conn.QueryRow(ctx, `SELECT inventory.create_item_movement_proc(
		$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::bigint, $6::text,
		$7::uuid, $8::uuid, $9::int, $10::uuid, $11::text, $12::jsonb,
		$13::uuid, $14::uuid
	)`,
		tenantID,
		uuid.MustParse(itemID),
		uuid.MustParse(slotID), // p_from_id
		uuid.MustParse(boxID),  // p_to_id
		5,                      // p_quantity
		"gate-test",            // p_handler
		uuid.Nil,               // p_collection_id
		nil,                    // p_order_id
		nil,                    // p_position
		nil,                    // p_data_type_id
		nil,                    // p_data_type_slug
		nil,                    // p_data
		createdBy,              // p_created_by
		uuid.New(),             // p_movement_id
	).Scan(&movementID)
	require.NoError(t, err,
		"the gate must read the current row by version; created_at ordering rejects this move as insufficient")
	require.NotEqual(t, uuid.Nil, movementID)
}
