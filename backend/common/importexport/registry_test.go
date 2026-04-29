package importexport_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

func TestRegistryRegisterAndGet(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	err := reg.Register(&importexport.EntityDescriptor{
		TypeName:      "Location",
		Service:       "management",
		IdentityField: "name",
	})
	require.NoError(t, err)

	desc, ok := reg.Get("Location")
	require.True(t, ok)
	assert.Equal(t, "Location", desc.TypeName)
	assert.Equal(t, "management", desc.Service)
	assert.Equal(t, "name", desc.IdentityField)
}

func TestRegistryGetUnknownType(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()

	desc, ok := reg.Get("NonExistent")
	assert.False(t, ok)
	assert.Nil(t, desc)
}

func TestRegistryErrorOnEmptyTypeName(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()

	err := reg.Register(&importexport.EntityDescriptor{TypeName: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestRegistryErrorOnDuplicateRegistration(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{TypeName: "Location", Service: "management"}))

	err := reg.Register(&importexport.EntityDescriptor{TypeName: "Location", Service: "other"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate registration")
	assert.Contains(t, err.Error(), "Location")
}

func TestRegistryAll(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{TypeName: "Repository", Service: "inventory"}))
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{TypeName: "Device", Service: "management"}))
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{TypeName: "Location", Service: "management"}))

	all := reg.All()
	require.Len(t, all, 3)

	// Sorted alphabetically by TypeName.
	assert.Equal(t, "Device", all[0].TypeName)
	assert.Equal(t, "Location", all[1].TypeName)
	assert.Equal(t, "Repository", all[2].TypeName)
}

func TestRegistryTypeNames(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{TypeName: "Repository"}))
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{TypeName: "Device"}))
	require.NoError(t, reg.Register(&importexport.EntityDescriptor{TypeName: "Location"}))

	names := reg.TypeNames()
	assert.Equal(t, []string{"Device", "Location", "Repository"}, names)
}

func TestRegistryAllEmpty(t *testing.T) {
	t.Parallel()

	reg := importexport.NewRegistry()

	assert.Empty(t, reg.All())
	assert.Empty(t, reg.TypeNames())
}
