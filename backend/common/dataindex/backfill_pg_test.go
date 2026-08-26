package dataindex_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
)

// init disables testcontainers-go's ryuk reaper: it needs the Docker socket,
// which is permission-denied in sandboxed CI. The test terminates its own
// container instead.
//
//nolint:gochecknoinits
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true"); err != nil {
			panic("dataindex: disable ryuk: " + err.Error())
		}
	}
}

// TestBackfillListRenderingMatchesProject_Postgres runs the real BackfillSQL
// against Postgres and asserts the slot it writes is identical, element for
// element, to what the projection hook writes via Project for the same payload.
// A number that renders differently on the two paths lands in the slot as two
// different strings, and an overlaps query silently misses one population.
//
// Uses a throwaway Postgres container so it runs anywhere with Docker. Set
// SKIP_PG_TESTS=1 to opt out where Docker is unavailable -- an explicit escape,
// not a silent skip.
func TestBackfillListRenderingMatchesProject_Postgres(t *testing.T) {
	t.Parallel()

	if os.Getenv("SKIP_PG_TESTS") == "1" {
		t.Skip("SKIP_PG_TESTS=1")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if terr := container.Terminate(context.Background()); terr != nil {
			t.Logf("terminate container: %v", terr)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() {
		if cerr := conn.Close(ctx); cerr != nil {
			t.Logf("close conn: %v", cerr)
		}
	}()

	// Literal JSON numbers that stress the float rendering -- powers of two at the
	// int64 boundary, the shortest-round-trip and exponent cases -- plus a string
	// and a bool for the whole element contract. Both paths start from this text.
	const payload = `{"l":[
		"SN-STRING", true,
		0, 1.5, -2.5, 123.456, 123456789,
		9007199254740992, 1152921504606846976, 9223372036854775808,
		1234567890123456, 0.30000000000000004, 1e20, 1e-10
	]}`

	table := "dataindex_render_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = conn.Exec(ctx, `CREATE TABLE public.`+table+` (
		id uuid PRIMARY KEY, tenant_id uuid, data_type_slug text, data jsonb, data_ix_list1 jsonb)`)
	require.NoError(t, err)

	tenantID, rowID := uuid.New(), uuid.New()
	_, err = conn.Exec(ctx,
		`INSERT INTO public.`+table+` (id, tenant_id, data_type_slug, data) VALUES ($1, $2, 'slug', $3::jsonb)`,
		rowID, tenantID, payload)
	require.NoError(t, err)

	binding := dataindex.Binding{Source: "/l", Slot: "data_ix_list1"}

	stmt, err := dataindex.BackfillSQL("public", table, binding, 100)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, stmt, tenantID, "slug", uuid.Nil)
	require.NoError(t, err)

	var slotJSON string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT data_ix_list1::text FROM public.`+table+` WHERE id = $1`, rowID).Scan(&slotJSON))
	var fromSQL []string
	require.NoError(t, json.Unmarshal([]byte(slotJSON), &fromSQL))

	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &data))
	projected, err := dataindex.Project(data, binding)
	require.NoError(t, err)
	fromGo, ok := projected.([]string)
	require.True(t, ok)

	require.Equal(t, fromGo, fromSQL, "backfill and Project must render every element identically")
}
