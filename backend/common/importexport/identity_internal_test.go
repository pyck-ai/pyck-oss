package importexport

import "testing"

func TestEntityDescriptorIdentity(t *testing.T) {
	t.Parallel()

	desc := &EntityDescriptor{TypeName: "DataType", IdentityFields: []string{"slug", "version"}}

	t.Run("complete composite identity", func(t *testing.T) {
		t.Parallel()
		where, key, ok := desc.identity(map[string]any{"slug": "widget", "version": float64(2), "extra": "ignored"})
		if !ok {
			t.Fatal("expected ok for complete identity")
		}
		if where["slug"] != "widget" || where["version"] != float64(2) {
			t.Errorf("where = %v, want {slug:widget, version:2}", where)
		}
		if _, has := where["extra"]; has {
			t.Error("where must contain only identity fields")
		}
		if key != "widget"+identityKeySep+"2" {
			t.Errorf("key = %q, want composite key", key)
		}
	})

	t.Run("missing field → not ok", func(t *testing.T) {
		t.Parallel()
		if _, _, ok := desc.identity(map[string]any{"slug": "widget"}); ok {
			t.Error("expected !ok when a field is absent")
		}
	})

	t.Run("explicit nil field → not ok (no {version: nil} filter)", func(t *testing.T) {
		t.Parallel()
		if _, _, ok := desc.identity(map[string]any{"slug": "widget", "version": nil}); ok {
			t.Error("expected !ok when a field is nil")
		}
	})

	t.Run("no identity fields → not ok", func(t *testing.T) {
		t.Parallel()
		empty := &EntityDescriptor{TypeName: "Customer"}
		if _, _, ok := empty.identity(map[string]any{"id": "x"}); ok {
			t.Error("expected !ok for create-only entity with no identity fields")
		}
	})
}
