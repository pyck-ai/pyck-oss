package importexport_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

// fakeDescriptor creates an EntityDescriptor backed by an in-memory store.
// The identity field is always "name".
func fakeDescriptor(typeName string) (*importexport.EntityDescriptor, *[]map[string]any) {
	store := &[]map[string]any{}
	nextID := 0

	return &importexport.EntityDescriptor{
		TypeName:      typeName,
		Service:       "test",
		IdentityField: "name",
		List: func(_ context.Context, _ *string, _ *int, where map[string]any) (importexport.ListResult, error) {
			var nodes []map[string]any
			for _, e := range *store {
				if where != nil {
					match := true
					for k, v := range where {
						if e[k] != v {
							match = false
							break
						}
					}
					if !match {
						continue
					}
				}
				nodes = append(nodes, e)
			}
			return importexport.ListResult{Nodes: nodes}, nil
		},
		Create: func(_ context.Context, input map[string]any) (map[string]any, error) {
			nextID++
			entity := make(map[string]any)
			for k, v := range input {
				entity[k] = v
			}
			entity["id"] = fmt.Sprintf("id-%d", nextID)
			*store = append(*store, entity)
			return entity, nil
		},
		Update: func(_ context.Context, id string, input map[string]any) (map[string]any, error) {
			for i, e := range *store {
				if e["id"] == id {
					for k, v := range input {
						(*store)[i][k] = v
					}
					return (*store)[i], nil
				}
			}
			return nil, fmt.Errorf("not found: %s", id)
		},
	}, store
}

// fakeDescriptorWithData creates a descriptor pre-populated with entities.
// The identity field is always "name".
func fakeDescriptorWithData(typeName string, data []map[string]any) *importexport.EntityDescriptor {
	desc, store := fakeDescriptor(typeName)
	*store = data
	return desc
}

func writeJSONL(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
