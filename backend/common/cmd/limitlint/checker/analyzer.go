// Package checker provides a go/analysis Analyzer that flags .All(ctx)
// calls on Ent query builders for entities that carry LimitMixin.
//
// LimitMixin silently caps query results at 200 rows. When a caller uses
// .All(ctx) without an explicit .Limit() in the chain, rows are dropped
// without error. This analyzer catches the pattern at build time and
// suggests .AllPages(ctx, mixin.Limit) as the fix.
package checker

import (
	"errors"
	"fmt"
	"go/ast"
	"go/types"
	"regexp"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// errInspectResultType means inspect.Analyzer produced a value that
// is not a *inspector.Inspector. This should never happen with the
// standard go/analysis driver; the check exists only to satisfy
// forcetypeassert without panicking.
var errInspectResultType = errors.New("inspect.Analyzer result is not *inspector.Inspector")

// allowMarkerRe matches the //limitlint:allow opt-out token with word
// boundaries on both sides — so `limitlint:allowed` or
// `limitlint:allowall` are NOT treated as the marker.
var allowMarkerRe = regexp.MustCompile(`\blimitlint:allow\b`)

// defaultEntGenPackageSuffix is the import path suffix every real
// pyck ent/gen package ends with.
const defaultEntGenPackageSuffix = "/ent/gen"

// Config wires the analyzer to a source of LimitMixin facts.
type Config struct {
	// IsLimitMixin returns true when the given entity in the given
	// import path embeds LimitMixin. Implementations may key on entity
	// name only when the call sites and schemas live in the same module.
	IsLimitMixin func(importPath, entity string) bool

	// EntGenPackageSuffix is the import path suffix used to recognise
	// generated ent packages. Production callers leave this empty —
	// the analyzer falls back to "/ent/gen". Tests may override the
	// suffix when their testdata cannot be named literally "gen" (the
	// project-wide `task clean` step would otherwise wipe such a
	// directory).
	EntGenPackageSuffix string
}

// New returns a configured Analyzer.
func New(cfg Config) *analysis.Analyzer {
	if cfg.IsLimitMixin == nil {
		cfg.IsLimitMixin = func(string, string) bool { return false }
	}
	if cfg.EntGenPackageSuffix == "" {
		cfg.EntGenPackageSuffix = defaultEntGenPackageSuffix
	}
	return &analysis.Analyzer{
		Name:     "limitlint",
		Doc:      "flags .All(ctx) on Ent queries for LimitMixin entities without explicit .Limit()",
		Requires: []*analysis.Analyzer{inspect.Analyzer},
		Run: func(pass *analysis.Pass) (any, error) {
			return run(pass, cfg)
		},
	}
}

func run(pass *analysis.Pass, cfg Config) (any, error) {
	// Generated ent code legitimately calls .All(ctx) — the AllPages
	// helper itself does. Skip the whole package when its path matches
	// the generated layout.
	if pass.Pkg != nil && strings.HasSuffix(pass.Pkg.Path(), cfg.EntGenPackageSuffix) {
		return nil, nil //nolint:nilnil // analysis.Analyzer.Run: (result any, err error); no result here.
	}

	allowedLines := collectAllowComments(pass)

	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, fmt.Errorf("%w: got %T", errInspectResultType, pass.ResultOf[inspect.Analyzer])
	}

	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "All" {
			return
		}
		if len(call.Args) != 1 {
			return
		}
		// The single argument must be a context.Context. This filters
		// out unrelated .All(...) methods on other types.
		argType := pass.TypesInfo.TypeOf(call.Args[0])
		if argType == nil || !isContext(argType) {
			return
		}

		// The receiver of .All(ctx) must be a *<pkg>.<Entity>Query
		// from an ent/gen package.
		recvType := pass.TypesInfo.TypeOf(sel.X)
		importPath, entity, ok := entityFromQueryType(recvType, cfg.EntGenPackageSuffix)
		if !ok {
			return
		}
		if !cfg.IsLimitMixin(importPath, entity) {
			return
		}

		// Walk up the chained selector calls. If any chained call is
		// .Limit(...), the caller paginated explicitly and we leave
		// the call alone.
		if hasLimitInChain(sel.X) {
			return
		}

		// Honor an explicit opt-out marker: //limitlint:allow on the
		// same line as the .All call, or on the line immediately above.
		// Use sparingly — the marker only makes sense when the caller
		// has independently bounded the result set or is exercising the
		// cap behavior intentionally (see backend/common/ent/mixin/
		// limit_mixin_test.go).
		pos := pass.Fset.Position(call.Pos())
		if allowedLines[lineKey{file: pos.Filename, line: pos.Line}] {
			return
		}

		pass.Report(analysis.Diagnostic{
			Pos: call.Pos(),
			End: call.End(),
			Message: "unsafe .All(ctx) on " + entity +
				" — entity has LimitMixin (200-row silent cap); " +
				"use .AllPages(ctx, mixin.Limit) or add an explicit .Limit(...)",
		})
	})

	return nil, nil //nolint:nilnil // analysis.Analyzer.Run: (result any, err error); no result here.
}

// isContext reports whether t is context.Context (or an alias).
func isContext(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		// Interface types in Go 1.21+ may also appear as *types.Interface
		// without a Named wrapper; fall back to a structural check.
		if iface, ok := t.Underlying().(*types.Interface); ok {
			// context.Context has Deadline, Done, Err, Value.
			return iface.NumMethods() >= 4 && hasContextMethods(iface)
		}
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

func hasContextMethods(iface *types.Interface) bool {
	want := map[string]bool{"Deadline": true, "Done": true, "Err": true, "Value": true}
	for i := range iface.NumMethods() {
		if want[iface.Method(i).Name()] {
			delete(want, iface.Method(i).Name())
		}
	}
	return len(want) == 0
}

type lineKey struct {
	file string
	line int
}

// collectAllowComments records every line that carries a //limitlint:allow
// marker. The marker is honored when it sits on the call's own line or on
// the line immediately above it. A blank line between marker and call
// breaks the association — keep them adjacent. We pre-build the set so the
// per-call check is O(1).
func collectAllowComments(pass *analysis.Pass) map[lineKey]bool {
	allowed := map[lineKey]bool{}
	for _, file := range pass.Files {
		for _, group := range file.Comments {
			for _, c := range group.List {
				if !allowMarkerRe.MatchString(c.Text) {
					continue
				}
				pos := pass.Fset.Position(c.Pos())
				allowed[lineKey{file: pos.Filename, line: pos.Line}] = true
				allowed[lineKey{file: pos.Filename, line: pos.Line + 1}] = true
			}
		}
	}
	return allowed
}

// entityFromQueryType inspects the receiver type of an .All call and
// returns (importPath, entityName) when it is a *<pkg>.<Entity>Query
// whose package ends in entGenSuffix. Returns ok=false otherwise.
func entityFromQueryType(t types.Type, entGenSuffix string) (string, string, bool) {
	if t == nil {
		return "", "", false
	}
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return "", "", false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return "", "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return "", "", false
	}
	pkgPath := obj.Pkg().Path()
	if !strings.HasSuffix(pkgPath, entGenSuffix) {
		return "", "", false
	}
	name := obj.Name()
	if !strings.HasSuffix(name, "Query") {
		return "", "", false
	}
	entity := strings.TrimSuffix(name, "Query")
	if entity == "" {
		return "", "", false
	}
	return pkgPath, entity, true
}

// hasLimitInChain walks the receiver expression of .All looking for an
// explicit .Limit(...) call somewhere in the same chained-method
// expression. It only inspects direct chain links (CallExpr → SelectorExpr
// → CallExpr → …); it does not chase Limit calls hidden inside arguments
// or behind intermediate variables.
func hasLimitInChain(expr ast.Expr) bool {
	for {
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if sel.Sel.Name == "Limit" {
			return true
		}
		expr = sel.X
	}
}
