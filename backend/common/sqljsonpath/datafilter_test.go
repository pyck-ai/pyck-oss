package sqljsonpath_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/sqljsonpath"
)

func TestPathValue(t *testing.T) {
	t.Parallel()

	path, value, err := sqljsonpath.PathValue([]string{"meta.name", "x"})
	require.NoError(t, err)
	assert.NotNil(t, path)
	assert.Equal(t, "x", value)

	for _, data := range [][]string{nil, {}, {"meta.name"}, {"meta.name", "x", "y"}} {
		_, _, err := sqljsonpath.PathValue(data)
		require.ErrorIs(t, err, sqljsonpath.ErrInvalidDataFilter, "%q", data)
	}

	_, _, err = sqljsonpath.PathValue([]string{"x' OR '1'='1", "x"})
	require.ErrorIs(t, err, sqljsonpath.ErrInvalidJSONPath, "the path is still validated")
}

func TestPathValues(t *testing.T) {
	t.Parallel()

	path, values, err := sqljsonpath.PathValues([]string{"type", "a", "b"})
	require.NoError(t, err)
	assert.NotNil(t, path)
	assert.Equal(t, []any{"a", "b"}, values)

	for _, data := range [][]string{nil, {}, {"type"}} {
		_, _, err := sqljsonpath.PathValues(data)
		require.ErrorIs(t, err, sqljsonpath.ErrInvalidDataFilter, "%q", data)
	}

	_, _, err = sqljsonpath.PathValues([]string{"x'--", "a"})
	require.ErrorIs(t, err, sqljsonpath.ErrInvalidJSONPath, "the path is still validated")
}

func TestKeyPath(t *testing.T) {
	t.Parallel()

	path, err := sqljsonpath.KeyPath("meta.name")
	require.NoError(t, err)
	assert.NotNil(t, path)

	_, err = sqljsonpath.KeyPath("")
	require.ErrorIs(t, err, sqljsonpath.ErrInvalidDataFilter)

	_, err = sqljsonpath.KeyPath("x'")
	require.ErrorIs(t, err, sqljsonpath.ErrInvalidJSONPath, "the key is still validated")
}

// FuzzPathValue checks the helpers never return a path together with an error
// or accept a shape the operator cannot use.
func FuzzPathValue(f *testing.F) {
	f.Add("meta.name", "x", "", 2)
	f.Add("", "", "", 0)
	f.Add("x'", "y", "z", 3)
	f.Fuzz(func(t *testing.T, a, b, c string, n int) {
		data := []string{a, b, c}[:min(max(n, 0), 3)]
		if path, _, err := sqljsonpath.PathValue(data); (err == nil) != (len(data) == 2 && sqljsonpath.Validate(a) == nil) || (err != nil && path != nil) {
			t.Fatalf("PathValue(%q) = path %v, err %v", data, path != nil, err)
		}
		if path, _, err := sqljsonpath.PathValues(data); (err == nil) != (len(data) >= 2 && sqljsonpath.Validate(a) == nil) || (err != nil && path != nil) {
			t.Fatalf("PathValues(%q) = path %v, err %v", data, path != nil, err)
		}
		if path, err := sqljsonpath.KeyPath(a); (err == nil) != (a != "" && sqljsonpath.Validate(a) == nil) || (err != nil && path != nil) {
			t.Fatalf("KeyPath(%q) = path %v, err %v", a, path != nil, err)
		}
	})
}
