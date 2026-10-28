package registertenant_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	registertenant "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// Org metadata is text: whatever the activity renders is what an operator
// (and every consumer of the tenant's metadata) reads back. The rendering
// used to fall back to %v, which prints a float64 in Go's shortest form
// (1e+06 for a million) so a tenant that registered with a seat count
// carried "1e+06" in its metadata forever.

// renderOrgMetadata pushes one key through the activity and returns the
// bytes Zitadel would have stored for it.
func renderOrgMetadata(t *testing.T, value any) (string, error) {
	t.Helper()
	f := newFakeZitadel()
	a := newZitadelActivities(f)

	err := a.SetOrgMetadataActivity(t.Context(), registertenant.SetOrgMetadataActivityInput{
		OrganizationID: "org-1",
		Data:           map[string]any{"k": value},
	})
	if err != nil {
		return "", err
	}
	require.NotNil(t, f.orgs.capturedMetadata)
	entries := f.orgs.capturedMetadata.GetMetadata()
	require.Len(t, entries, 1)
	return string(entries[0].GetValue()), nil
}

// Every scalar shape a JSON-decoded tenant-data map can hold renders as the
// text a human would write. The float cases are the reason the switch
// exists: none of them may come out in exponent notation.
func TestSetOrgMetadataActivity_RendersEveryScalarWithoutExponents(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"a string is passed through", "pyck-go", "pyck-go"},
		{"a whole number keeps all its digits", float64(1_000_000), "1000000"},
		{"a very large number is still written in full", float64(1e21), "1000000000000000000000"},
		{"a very small number is not an exponent either", float64(1e-7), "0.0000001"},
		{"a fractional number keeps its fraction", 0.5, "0.5"},
		{"a negative number keeps its sign", -12.25, "-12.25"},
		{"a zero is zero", float64(0), "0"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"a json.Number renders like the float64 it decodes to", json.Number("1000000"), "1000000"},
		// The decoder gqlgen's HTTP transport uses (UseNumber) keeps the
		// caller's own spelling, exponent and all. Rendering it verbatim
		// would put "1e6" in the metadata, the very shape the explicit
		// switch replaced %v to avoid.
		{"a json.Number in exponent form is spelled out", json.Number("1e6"), "1000000"},
		{"a negative json.Number exponent is spelled out", json.Number("-1.5e-7"), "-0.00000015"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := renderOrgMetadata(t, tc.value)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "e+", "org metadata is read back as text, never as a Go literal")
		})
	}
}

// A value the switch does not know is an error naming the key, not a Go
// struct dump silently shipped as a tenant's metadata. The resolver's
// validateTenantData refuses non-scalars before a row is written, so this
// is the invariant restated where the value is rendered: it is what keeps
// a future caller of the activity from re-opening the %v hole.
func TestSetOrgMetadataActivity_RefusesAValueItCannotRender(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{
		"a nested map": map[string]any{"tier": "gold"},
		"a slice":      []any{"eu", "us"},
		"a nil value":  nil,
		"an int":       12,
		"a struct":     struct{ A int }{A: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := renderOrgMetadata(t, value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `tenant data "k"`, "the refusal must name the offending key")
			assert.Contains(t, err.Error(), "not a scalar")
		})
	}
}

// The embedded definitions are compiled into the binary, so their parse
// result cannot change between calls: it is computed once per process and
// handed out, rather than re-read and re-decoded for every registration.
func TestDefaultDataTypeDefinitions_ParsedOncePerProcess(t *testing.T) {
	t.Parallel()

	first, err := registertenant.DefaultDataTypeDefinitions()
	require.NoError(t, err)
	require.NotEmpty(t, first, "the binary must embed at least one default definition")

	second, err := registertenant.DefaultDataTypeDefinitions()
	require.NoError(t, err)

	require.Len(t, second, len(first))
	assert.Same(t, &first[0], &second[0],
		"a second call must hand out the memoized slice, not re-parse the embedded files")
	for i := range first {
		assert.NotEmpty(t, first[i].Slug, "a definition without a slug would be re-created on every re-run")
	}
}
