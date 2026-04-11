package gen

import (
	"regexp"
	"strings"
)

// refPath represents a parsed $ref value.
//
// Examples:
//   - "res.status"                                  → {kind:"res", id:"",          subpath:"status"}
//   - "res.body.data.foo"                            → {kind:"res", id:"",          subpath:"body.data.foo"}
//   - "res[createFile].body.data.createFile.file.id" → {kind:"res", id:"createFile", subpath:"body.data.createFile.file.id"}
//   - "req.body.variables.input.name"               → {kind:"req", id:"",          subpath:"body.variables.input.name"}
type refPath struct {
	kind    string // "res" or "req"
	id      string // "" = current step; non-empty = cross-step ID
	subpath string // path after the kind+id prefix
}

var refRe = regexp.MustCompile(`^(res|req)(?:\[([^\]]+)\])?\.(.+)$`)

// parseRef parses a $ref string into a refPath.
// Returns (rp, true) on success, or (refPath{}, false) if the string does not
// match the res/req grammar.
func parseRef(refStr string) (refPath, bool) {
	m := refRe.FindStringSubmatch(strings.TrimSpace(refStr))
	if m == nil {
		return refPath{}, false
	}
	return refPath{kind: m[1], id: m[2], subpath: m[3]}, true
}
