package stock

import (
	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
)

// lowestCommonAncestor returns the deepest repo that is an ancestor of both
// fromID and toID via parent_id in reposMap. Returns uuid.Nil when the two
// repos do not share an ancestor (e.g., one virtual, one not). Returns
// fromID when fromID == toID. Returns the upper endpoint when one is an
// ancestor of the other. A revisited node terminates the walk and yields
// uuid.Nil, so a cyclic parent_id graph degrades gracefully rather than
// looping forever.
//
// The helper is package-private because only the simulate-walk and
// executor-walk in this package consume it; nothing outside service/stock
// references repo ancestry today.
func lowestCommonAncestor(reposMap map[uuid.UUID]ent.Repository, fromID, toID uuid.UUID) uuid.UUID {
	if fromID == toID {
		// A node is its own ancestor; short-circuit before touching the map so
		// the helper stays well-defined even when the (matching) endpoint is
		// absent from reposMap.
		return fromID
	}
	if _, ok := reposMap[fromID]; !ok {
		return uuid.Nil
	}
	if _, ok := reposMap[toID]; !ok {
		return uuid.Nil
	}

	// Collect fromID and all of its ancestors into a set. The seen-set
	// doubles as cycle protection: revisiting a node means the parent_id
	// graph has a cycle, so we bail out with uuid.Nil rather than looping
	// forever. There is no depth cap; real repo trees in the platform
	// rarely exceed ~10 levels but the test suite intentionally exercises
	// 50+ deep chains, and a hard cap there would silently mis-classify
	// internal moves as cross-tree.
	fromAncestors := make(map[uuid.UUID]struct{})
	cur := fromID
	for {
		if _, seen := fromAncestors[cur]; seen {
			// Cycle detected; treat as no shared ancestor rather than recursing
			// forever.
			return uuid.Nil
		}
		fromAncestors[cur] = struct{}{}
		repo, ok := reposMap[cur]
		if !ok || repo.ParentID == uuid.Nil {
			break
		}
		cur = repo.ParentID
	}

	// Walk toID's ancestors and return the first one present in fromAncestors.
	cur = toID
	visited := make(map[uuid.UUID]struct{})
	for {
		if _, seen := visited[cur]; seen {
			return uuid.Nil
		}
		visited[cur] = struct{}{}
		if _, hit := fromAncestors[cur]; hit {
			return cur
		}
		repo, ok := reposMap[cur]
		if !ok || repo.ParentID == uuid.Nil {
			return uuid.Nil
		}
		cur = repo.ParentID
	}
}

// lowestCommonAncestorTx existed in earlier phases as the executor-path
// counterpart to lowestCommonAncestor. Phase 4.3 removed every executor
// caller that lacked a pre-loaded repoMap, so the per-step
// tx.Repository.Get walk no longer has any callers and was deleted. The
// only LCA helper left is the pure-map lowestCommonAncestor above; new
// executor callers must populate repoMap via loadAncestorStocks before
// calling into the executor functions.
