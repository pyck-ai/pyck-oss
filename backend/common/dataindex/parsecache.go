package dataindex

import "sync"

// parseCacheLimit bounds the memo: one entry per datatype version in use, so the
// cap only guards a pathology. Cleared wholesale when hit — eviction order does
// not matter for a pure function.
const parseCacheLimit = 512

var (
	parseCacheMu sync.RWMutex
	parseCache   = map[string]parsedSchema{}
)

type parsedSchema struct {
	bindings Bindings
	err      error
}

// ParseCached is Parse memoized on the schema text. Parse is a pure function of
// that text, so no invalidation is needed: a datatype change produces a
// different key. For the paths that re-parse the same schema per write or per
// request, where the json.Unmarshal dominates.
//
// The returned Bindings is shared between callers and must not be modified.
func ParseCached(schema string) (Bindings, error) {
	parseCacheMu.RLock()
	hit, ok := parseCache[schema]
	parseCacheMu.RUnlock()
	if ok {
		return hit.bindings, hit.err
	}

	bindings, err := Parse(schema)

	parseCacheMu.Lock()
	if len(parseCache) >= parseCacheLimit {
		parseCache = make(map[string]parsedSchema, parseCacheLimit)
	}
	parseCache[schema] = parsedSchema{bindings: bindings, err: err}
	parseCacheMu.Unlock()

	return bindings, err
}
