package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/management/ent/gen"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	"github.com/stretchr/testify/assert"
)

func TestEventService(t *testing.T) {
	client := enttest.Open(
		t,
		dialect.SQLite,
		fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_fk=1", t.Name(), time.Now().UnixNano()),
		enttest.WithOptions(gen.Log(t.Log)),
	).Debug()

	testTenantID, _ := uuid.Parse("b98b88eb-ce77-4e9a-a224-d37443a9c5c1")
	testTopic := "test-topic"
	testName := "test-name"
	testExampleData := map[string]interface{}{
		"test": "data",
	}
	testUser := &authn.User{
		ID:       uuidgql.GenerateV7UUID(),
		TenantID: testTenantID,
		Roles: map[uuid.UUID]authn.Role{
			testTenantID: authn.ROLE_ADMIN,
		},
	}

	service, err := NewEventRegistryService(client)
	assert.NoError(t, err)
	assert.IsType(t, &EventRegistryService{}, service)

	t.Run("success save new event", func(t *testing.T) {
		ctx := tenant.Context(authn.Context(t.Context(), testUser), testTenantID)
		err := service.SaveEvent(ctx, testTopic, testName, testExampleData)
		assert.NoError(t, err)
		events, err := client.Event.Query().All(ctx)
		assert.NoError(t, err)
		assert.Len(t, events, 1)
	})

	t.Run("success preload cache events", func(t *testing.T) {
		ctx := tenant.Context(authn.Context(t.Context(), testUser), testTenantID)
		err = client.Event.
			Create().
			SetTopic("topic").
			SetName("name").
			Exec(ctx)
		assert.NoError(t, err)
		events, _ := client.Event.Query().All(ctx)
		err := service.PreloadEventsCache(t.Context())
		assert.NoError(t, err)
		for _, ev := range events {
			_, ok := service.memStore.Get(ev.Name)
			assert.True(t, ok)
		}
	})
}
