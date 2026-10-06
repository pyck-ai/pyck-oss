package resolvers

import (
	"reflect"
	"strings"
	"testing"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
)

// TestReservedEventTypesCoverEveryEntity fails when the entity list generated
// into backend/common/datatype lags behind the management schema, i.e. an
// entity was added without regenerating it. The generated client has one
// exported *<Entity>Client field per entity, named after the entity.
func TestReservedEventTypesCoverEveryEntity(t *testing.T) {
	t.Parallel()

	ct := reflect.TypeOf(ent.Client{})
	checked := 0
	for i := range ct.NumField() {
		f := ct.Field(i)
		if !f.IsExported() || f.Type.Kind() != reflect.Pointer || !strings.HasSuffix(f.Type.Elem().Name(), "Client") {
			continue
		}
		if !isReservedEventType(f.Name) {
			t.Errorf("management entity %s is not reserved; run go generate in backend/common/datatype", f.Name)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no entity clients found on ent.Client")
	}
}
