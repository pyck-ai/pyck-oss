package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBootstrapModule_UnmarshalText_Valid(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		text string
		want BootstrapModule
	}{
		{"zitadel", BootstrapModuleZitadel},
		{"temporal", BootstrapModuleTemporal},
		{"minio", BootstrapModuleMinio},
	} {
		var m BootstrapModule
		require.NoError(t, m.UnmarshalText([]byte(tc.text)))
		assert.Equal(t, tc.want, m)
	}
}

func TestBootstrapModule_UnmarshalText_Invalid(t *testing.T) {
	t.Parallel()

	var m BootstrapModule
	assert.Error(t, m.UnmarshalText([]byte("bogus")))
}
