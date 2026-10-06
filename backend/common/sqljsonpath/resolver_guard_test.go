package sqljsonpath_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperFor is the shared constructor each *WhereInput data filter resolver
// must call. The helper refuses a list length or key the operator cannot use;
// the hand-written bodies used to skip the predicate instead, which returned
// the caller's whole table.
var helperFor = map[string]string{
	"Data":         "sqljsonpath.PathValue(",
	"DataContains": "sqljsonpath.PathValue(",
	"DataIn":       "sqljsonpath.PathValues(",
	"DataHasKey":   "sqljsonpath.KeyPath(",
}

// TestDataFilterResolversUseTheSharedHelpers scans every service's gqlgen
// resolver files. gqlgen keeps these bodies across regeneration and no
// template writes them, so each new entity with a data column copies them by
// hand; this test keeps every copy on the helpers.
func TestDataFilterResolversUseTheSharedHelpers(t *testing.T) {
	t.Parallel()

	_, self, _, ok := runtime.Caller(0)
	require.True(t, ok)
	backend := filepath.Join(filepath.Dir(self), "..", "..")
	files, err := filepath.Glob(filepath.Join(backend, "*", "resolvers", "*.resolvers.go"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	found := 0
	for _, file := range files {
		src, err := os.ReadFile(file)
		require.NoError(t, err)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, 0)
		require.NoError(t, err, file)

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			want, isFilter := helperFor[fn.Name.Name]
			if !isFilter || !strings.HasSuffix(receiverType(fn), "WhereInputResolver") {
				continue
			}
			found++
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			rel, _ := filepath.Rel(backend, file) //nolint:errcheck // display only
			if !strings.Contains(body, want) {
				t.Errorf("%s: %s.%s must build its path with %s", rel, receiverType(fn), fn.Name.Name, want)
			}
		}
	}
	// 21 entities with a data column, four filters each.
	assert.Equal(t, 84, found, "the number of data filter resolvers changed; check the new ones use the helpers")
}

func receiverType(fn *ast.FuncDecl) string {
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}
