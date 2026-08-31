package api_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/importexport"

	"github.com/pyck-ai/pyck/backend/management/api"
)

// DataType must register an Update func wired to updateDataType: `name` is
// deliberately NOT in ImmutableFields because renaming in place is supported
// (createDataType's reconcile allows only name to differ on a re-import).
// Without an Update func the importer treats DataType as create-only and
// silently skips an existing (slug, version) — dropping a changed name, the
// same silent divergence checkImmutable exists to prevent.
func TestRegisterDataType_UpdateWiredForRename(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	require.NoError(t, api.RegisterEntities(reg, nil))

	desc, ok := reg.Get("DataType")
	require.True(t, ok)
	require.NotNil(t, desc.Update,
		"DataType needs an Update func so a re-imported name change reaches the server's rename branch instead of being skipped")
	require.NotContains(t, desc.ImmutableFields, "name",
		"name is mutable (rename-in-place); it must not be treated as immutable")
}
