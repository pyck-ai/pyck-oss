//nolint:testpackage // in-package test required: lowestCommonAncestor is package-private.
package stock

import (
	"testing"

	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// repoNode is a tiny constructor used only by lowestCommonAncestor's tests so
// each table case can be read without piles of struct literal noise.
func repoNode(id, parent uuid.UUID) ent.Repository {
	return ent.Repository{ID: id, ParentID: parent}
}

func TestLowestCommonAncestor(t *testing.T) {
	t.Parallel()

	// Stable, named UUIDs so test output points at the offending node.
	a := uuid.New()
	b := uuid.New()
	c := uuid.New()
	d := uuid.New()
	e := uuid.New()
	x := uuid.New()
	y := uuid.New()
	missing := uuid.New()

	// linear: a -> b -> c (b is parent of c, a is parent of b, a is root).
	linear := map[uuid.UUID]ent.Repository{
		a: repoNode(a, uuid.Nil),
		b: repoNode(b, a),
		c: repoNode(c, b),
	}

	// siblings: a is root; b and c both have parent a.
	siblings := map[uuid.UUID]ent.Repository{
		a: repoNode(a, uuid.Nil),
		b: repoNode(b, a),
		c: repoNode(c, a),
	}

	// nephew: a -> b -> d, a -> c -> e (deep cousins under shared root a).
	nephew := map[uuid.UUID]ent.Repository{
		a: repoNode(a, uuid.Nil),
		b: repoNode(b, a),
		c: repoNode(c, a),
		d: repoNode(d, b),
		e: repoNode(e, c),
	}

	// twoRoots: x and y are unrelated roots (mimics virtual vs non-virtual
	// trees that share no ancestor).
	twoRoots := map[uuid.UUID]ent.Repository{
		x: repoNode(x, uuid.Nil),
		y: repoNode(y, uuid.Nil),
	}

	// cycle: two disjoint self-loop islands. Each side is its own
	// parent (a one-node cycle), so walking up never terminates and the
	// two sides never share an ancestor. A naive walker without a depth
	// cap loops forever; the seen-set guard must return uuid.Nil instead.
	cycleA := uuid.New()
	cycleB := uuid.New()
	cycle := map[uuid.UUID]ent.Repository{
		cycleA: repoNode(cycleA, cycleA),
		cycleB: repoNode(cycleB, cycleB),
	}

	// twoCycle: a two-node cycle (cyc1 -> cyc2 -> cyc1) on one side, with
	// an unrelated repo on the other. Exercises the multi-step branch of
	// the seen-set guard.
	cyc1 := uuid.New()
	cyc2 := uuid.New()
	cycOther := uuid.New()
	twoCycle := map[uuid.UUID]ent.Repository{
		cyc1:     repoNode(cyc1, cyc2),
		cyc2:     repoNode(cyc2, cyc1),
		cycOther: repoNode(cycOther, uuid.Nil),
	}

	// deepChain: a single linear chain root -> n1 -> n2 -> ... -> nLast,
	// 100 levels deep. The previous depth-cap implementation truncated
	// at 32 and would mis-report uuid.Nil for the LCA of the leaf and
	// its near-root ancestor; the seen-set-only walk must find the
	// expected node.
	const deepChainDepth = 100
	deepChain := make(map[uuid.UUID]ent.Repository, deepChainDepth)
	deepIDs := make([]uuid.UUID, deepChainDepth)
	for i := range deepIDs {
		deepIDs[i] = uuid.New()
	}
	for i, id := range deepIDs {
		parent := uuid.Nil
		if i > 0 {
			parent = deepIDs[i-1]
		}
		deepChain[id] = repoNode(id, parent)
	}
	deepRoot := deepIDs[0]
	deepLeaf := deepIDs[deepChainDepth-1]
	deepMid := deepIDs[deepChainDepth/2]

	cases := []struct {
		name  string
		repos map[uuid.UUID]ent.Repository
		from  uuid.UUID
		to    uuid.UUID
		want  uuid.UUID
	}{
		{
			name:  "identical_returns_self",
			repos: linear,
			from:  a,
			to:    a,
			want:  a,
		},
		{
			name:  "linear_ancestor_from_above_to_below",
			repos: linear,
			from:  b,
			to:    c,
			want:  b,
		},
		{
			name:  "linear_ancestor_from_below_to_above",
			repos: linear,
			from:  c,
			to:    b,
			want:  b,
		},
		{
			name:  "siblings_share_parent",
			repos: siblings,
			from:  b,
			to:    c,
			want:  a,
		},
		{
			name:  "deep_nephews_share_root",
			repos: nephew,
			from:  d,
			to:    e,
			want:  a,
		},
		{
			name:  "disjoint_roots_returns_nil",
			repos: twoRoots,
			from:  x,
			to:    y,
			want:  uuid.Nil,
		},
		{
			name:  "from_missing_from_map_returns_nil",
			repos: linear,
			from:  missing,
			to:    c,
			want:  uuid.Nil,
		},
		{
			name:  "to_missing_from_map_returns_nil",
			repos: linear,
			from:  c,
			to:    missing,
			want:  uuid.Nil,
		},
		{
			name:  "self_loop_cycle_returns_nil",
			repos: cycle,
			from:  cycleA,
			to:    cycleB,
			want:  uuid.Nil,
		},
		{
			name:  "two_node_cycle_returns_nil",
			repos: twoCycle,
			from:  cyc1,
			to:    cycOther,
			want:  uuid.Nil,
		},
		{
			name:  "deep_chain_leaf_to_mid_finds_mid",
			repos: deepChain,
			from:  deepLeaf,
			to:    deepMid,
			want:  deepMid,
		},
		{
			name:  "deep_chain_leaf_to_root_finds_root",
			repos: deepChain,
			from:  deepLeaf,
			to:    deepRoot,
			want:  deepRoot,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := lowestCommonAncestor(tc.repos, tc.from, tc.to)
			if got != tc.want {
				t.Fatalf("lowestCommonAncestor(from=%s, to=%s) = %s, want %s",
					tc.from, tc.to, got, tc.want)
			}
		})
	}
}
