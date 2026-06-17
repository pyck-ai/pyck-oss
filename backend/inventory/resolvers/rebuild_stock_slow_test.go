package resolvers_test

import "testing"

// TestRebuildInventoryStock_FuzzViaMovementFlow_Slow runs the heavy, long-running
// fuzz seeds (tens of seconds to minutes each). The "_Slow" suffix is
// load-bearing: `task test:slow` selects this test (and any other slow test)
// with -run '_Slow$', and that is the only job that runs it (main + nightly,
// .github/workflows/run-tests-slow.yml). The testing.Short() guard additionally
// skips it in the fast PR gate (`task test` / `task test:fast`, which pass
// -short), so it never runs there. The shared fuzzSeedCase type, runFuzzSeeds
// helper, fuzzConfig, and runFuzzRebuildTestViaMovementFlow live in
// rebuild_stock_test.go.
func TestRebuildInventoryStock_FuzzViaMovementFlow_Slow(t *testing.T) {
	if testing.Short() {
		t.Skip("slow fuzz seeds; run via `task test:slow` (omit -short)")
	}
	t.Parallel()

	runFuzzSeeds(t, []fuzzSeedCase{
		// seed=42 complexity=3 — same seed but more complex tree
		{"seed-42-c3", fuzzConfig{
			Seed: 42, Complexity: 3, Iterations: 60, ItemCount: 4, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 4, MaxLeaves: 24,
			ExecuteRatio: 0.64921134441865302, DynamicProb: 0.3,
			SeedQtyMin: 504, SeedQtyMax: 597, MoveQtyMin: 17, MoveQtyMax: 32,
			AllowMixedTrees: false,
		}},

		// seed=99 complexity=4 — deep tree (3-5 levels), many repos, 80 iterations
		{"seed-99-c4", fuzzConfig{
			Seed: 99, Complexity: 4, Iterations: 80, ItemCount: 5, RootCount: 4,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 4, MinDepth: 3, MaxDepth: 5, MaxLeaves: 32,
			ExecuteRatio: 0.60543104582618790, DynamicProb: 0.3,
			SeedQtyMin: 651, SeedQtyMax: 825, MoveQtyMin: 30, MoveQtyMax: 32,
			AllowMixedTrees: false,
		}},

		// ── Pagination boundary tests ─────────────────────────────────────────────
		// Goal: verify that paginated queries in RebuildStockTable and
		// GetRepositoriesDetails collect ALL records when total > 200 (LimitMixin cap).

		// ~20 repos × 11 items = ~220 seed moves — just over the 200-row page boundary.
		{"pagination-over-200-movements", fuzzConfig{
			Seed: 555, Complexity: 2, Iterations: 10, ItemCount: 11, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 20,
			ExecuteRatio: 1.0, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// ~20 repos × 15 items = ~300 seed moves + 50 random = ~350 total.
		// Tests rebuild spanning multiple pagination pages.
		{"pagination-multi-page-350", fuzzConfig{
			Seed: 777, Complexity: 3, Iterations: 50, ItemCount: 15, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 20,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 500, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// ── Execute ratio extremes ────────────────────────────────────────────────

		// 100% executed: fully settled state, no incoming/outgoing.
		{"all-executed-no-pending", fuzzConfig{
			Seed: 111, Complexity: 2, Iterations: 60, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 1.0, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// 0% executed: all random moves are pending reservations (incoming/outgoing only).
		{"all-random-pending-no-exec", fuzzConfig{
			Seed: 222, Complexity: 2, Iterations: 60, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 0.0, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 5, MoveQtyMax: 10,
			AllowMixedTrees: false,
		}},

		// ~25% of random moves are deleted — rebuild must ignore them entirely.
		{"high-delete-rate", fuzzConfig{
			Seed: 333, Complexity: 2, Iterations: 60, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 2, MinDepth: 2, MaxDepth: 3, MaxLeaves: 16,
			ExecuteRatio: 0.0, DynamicProb: 0.0, // 0% exec → ~25% deleted, 75% pending
			SeedQtyMin: 400, SeedQtyMax: 500, MoveQtyMin: 5, MoveQtyMax: 10,
			AllowMixedTrees: false,
		}},

		// ~65% executed, ~9% deleted, ~26% pending.
		{"mixed-exec-delete-pending", fuzzConfig{
			Seed: 444, Complexity: 3, Iterations: 60, ItemCount: 4, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 24,
			ExecuteRatio: 0.65, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// ── Tree structure extremes ───────────────────────────────────────────────

		// Many roots (15), shallow tree — tests broad stock aggregation.
		{"many-roots-shallow", fuzzConfig{
			Seed: 20, Complexity: 2, Iterations: 40, ItemCount: 3, RootCount: 15,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 2, MinDepth: 1, MaxDepth: 2, MaxLeaves: 60,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 200, SeedQtyMax: 300, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// Many nodes: large breadth (4) across 4 levels → many intermediate nodes.
		// Tests that stock aggregation is correct at every level of a bushy tree.
		{"many-nodes-wide-breadth", fuzzConfig{
			Seed: 60, Complexity: 3, Iterations: 50, ItemCount: 3, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 4, MaxBreadth: 4, MinDepth: 2, MaxDepth: 4, MaxLeaves: 50,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// Many leaves: MaxLeaves=60 with multiple roots and breadth.
		// All leaves receive seed stock; tests per-leaf and aggregated parent stock.
		{"many-leaves", fuzzConfig{
			Seed: 70, Complexity: 3, Iterations: 40, ItemCount: 2, RootCount: 4,
			VirtualTreeCount: 1, MinBreadth: 3, MaxBreadth: 4, MinDepth: 2, MaxDepth: 3, MaxLeaves: 60,
			ExecuteRatio: 0.8, DynamicProb: 0.0,
			SeedQtyMin: 200, SeedQtyMax: 300, MoveQtyMin: 10, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// Many branches (MaxBreadth=5): tests correct fan-out at each level.
		{"many-branches-per-level", fuzzConfig{
			Seed: 80, Complexity: 3, Iterations: 40, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 5, MaxBreadth: 5, MinDepth: 2, MaxDepth: 3, MaxLeaves: 50,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 20,
			AllowMixedTrees: false,
		}},

		// Many items (15 items × ~12 repos = ~180 seed moves): tests per-item
		// isolation and correct stock tracking for many distinct item types.
		{"many-items", fuzzConfig{
			Seed: 90, Complexity: 2, Iterations: 30, ItemCount: 15, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 2, MaxDepth: 3, MaxLeaves: 12,
			ExecuteRatio: 0.75, DynamicProb: 0.0,
			SeedQtyMin: 200, SeedQtyMax: 300, MoveQtyMin: 5, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// Dynamic repos only: all repos moveable (DynamicProb=1.0).
		// Tests rewind+replay of reparenting during rebuild.
		{"fully-dynamic-repos", fuzzConfig{
			Seed: 30, Complexity: 2, Iterations: 30, ItemCount: 2, RootCount: 2,
			VirtualTreeCount: 1, MinBreadth: 1, MaxBreadth: 2, MinDepth: 2, MaxDepth: 3, MaxLeaves: 12,
			ExecuteRatio: 0.75, DynamicProb: 1.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// Multiple virtual injection sources (3 virtual trees).
		{"multiple-virtual-trees", fuzzConfig{
			Seed: 50, Complexity: 2, Iterations: 40, ItemCount: 3, RootCount: 2,
			VirtualTreeCount: 3, MinBreadth: 1, MaxBreadth: 2, MinDepth: 2, MaxDepth: 3, MaxLeaves: 12,
			ExecuteRatio: 0.7, DynamicProb: 0.0,
			SeedQtyMin: 300, SeedQtyMax: 400, MoveQtyMin: 10, MoveQtyMax: 15,
			AllowMixedTrees: false,
		}},

		// ── Combined stress tests ─────────────────────────────────────────────────

		// Deep tree + many items + mixed execute/delete/pending + dynamic repos.
		// High item count pushes toward pagination boundaries.
		{"stress-combined-deep-mixed", fuzzConfig{
			Seed: 9999, Complexity: 4, Iterations: 80, ItemCount: 8, RootCount: 4,
			VirtualTreeCount: 2, MinBreadth: 2, MaxBreadth: 3, MinDepth: 3, MaxDepth: 4, MaxLeaves: 30,
			ExecuteRatio: 0.65, DynamicProb: 0.2,
			SeedQtyMin: 500, SeedQtyMax: 700, MoveQtyMin: 15, MoveQtyMax: 30,
			AllowMixedTrees: false,
		}},

		// Diverse seed, moderate complexity.
		{"stress-seed-12345", fuzzConfig{
			Seed: 12345, Complexity: 3, Iterations: 60, ItemCount: 5, RootCount: 3,
			VirtualTreeCount: 1, MinBreadth: 2, MaxBreadth: 3, MinDepth: 3, MaxDepth: 4, MaxLeaves: 24,
			ExecuteRatio: 0.75, DynamicProb: 0.3,
			SeedQtyMin: 400, SeedQtyMax: 600, MoveQtyMin: 10, MoveQtyMax: 25,
			AllowMixedTrees: false,
		}},
	})
}
