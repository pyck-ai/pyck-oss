package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractEntities(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write := func(service, name, src string) {
		dir := filepath.Join(root, service, "ent", "schema")
		require.NoError(t, os.MkdirAll(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600))
	}

	write("management", "datatype.go", `package schema
type DataType struct{ ent.Schema }
func (DataType) Mixin() []ent.Mixin { return []ent.Mixin{mixin.TenantMixin{}} }
`)
	write("management", "device.go", `package schema
type Device struct{ ent.Schema }
func (Device) Mixin() []ent.Mixin { return []ent.Mixin{mixin.DataMixin{}} }
`)
	// Privacy rule types live next to the schemas but are not entities.
	write("management", "device_policy.go", `package schema
type allowIfReader struct{}
`)
	write("inventory", "item.go", `package schema
type Item struct {
	ent.Schema
}
func (Item) Mixin() []ent.Mixin { return []ent.Mixin{mixin.DataMixin{}} }
`)

	dataTypeEntities, serviceEntities, err := extractEntities(filepath.Join(root, "*", "ent", "schema", "*.go"))
	require.NoError(t, err)

	assert.Equal(t, []string{"device", "item"}, dataTypeEntities)
	assert.Equal(t, map[string][]string{
		"management": {"DataType", "Device"},
		"inventory":  {"Item"},
	}, serviceEntities)
}
