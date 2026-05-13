package resolvers_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otelapi "go.opentelemetry.io/otel"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"

	ent "github.com/pyck-ai/pyck/backend/file/ent/gen"
	"github.com/pyck-ai/pyck/backend/file/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/file/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/file/ent/gen/file"
	"github.com/pyck-ai/pyck/backend/file/resolvers"
	"github.com/pyck-ai/pyck/backend/file/services"
)

// =============================================================================
// TEST SETUP
// =============================================================================

func TestMain(m *testing.M) {
	cleanup := resolver.MustSetupTestTracer("file")
	defer cleanup()
	os.Exit(m.Run())
}

// =============================================================================
// FIXTURES
// =============================================================================

var (
	tenantA = resolver.TenantA
	userA   = resolver.UserA
	userB   = resolver.UserB

	fileDataTypeID   = uuid.MustParse("94a80c62-3f81-4808-8961-768824d2c325")
	fileDataTypeSlug = "file"

	testRefID   = uuid.MustParse("cd4a12c2-87ed-4497-9a7f-5b1672c8f23d")
	testRefType = file.ReftypeSupplier

	validFileData = map[string]any{
		"type": "supplier",
		"meta": map[string]any{
			"name": "Testfile",
			"tags": []any{"foo", "bla"},
		},
	}
)

// =============================================================================
// TEST ENVIRONMENT
// =============================================================================

type testEnv struct {
	*resolver.TestEnvironment[*ent.Client]
	t         *testing.T
	mockMinio *mocks.MockMinioClient
}

func setup(t *testing.T) *testEnv {
	t.Helper()

	mockMinio := mocks.NewMockMinioClient()

	te := &testEnv{
		TestEnvironment: resolver.NewTestEnvironment[*ent.Client](t),
		t:               t,
		mockMinio:       mockMinio,
	}

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(ent.Log(t.Log)),
	).Debug()

	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "file",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(ent.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(ent.TxFromContext),
	}))

	mockedS3StorageService := &services.S3StorageService{
		Bucket:       "pyck-local-dev",
		AccessKey:    "test-access-key",
		SecretKey:    "test-secret-key",
		HTTPScheme:   "http",
		HTTPEndpoint: "endpoint",
		MinioClient:  mockMinio,
	}

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("file", client, v, mockedS3StorageService, te.WorkflowClient)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, ent.NewTxContext, "file-test", 0))
	})

	// Register data type for validation
	jsonSchema, _ := test.LoadSchemaByName("file")
	te.DataTypeProvider.AddDataType(json_schema.DataType{
		ID:         fileDataTypeID,
		Slug:       fileDataTypeSlug,
		TenantID:   tenantA,
		JsonSchema: string(jsonSchema),
	})

	return te
}

// ctx creates a request context for the given user.
func (te *testEnv) ctx(user *authn.User) context.Context {
	te.t.Helper()
	ctx := request.Context(te.t.Context(), user, user.TenantID)
	tracer := otelapi.Tracer("file-test")
	ctx, span := tracer.Start(ctx, "test")
	te.t.Cleanup(func() { span.End() })
	return ctx
}

// ctxWithDeleted creates a context that includes soft-deleted records.
func (te *testEnv) ctxWithDeleted(user *authn.User) context.Context {
	return feature.Context(te.ctx(user), feature.FEATURE_SHOW_DELETED)
}

// =============================================================================
// GRAPHQL EXECUTION HELPERS
// =============================================================================

// execOK executes a GraphQL query and asserts no errors.
func execOK[T any](te *testEnv, ctx context.Context, tpl resolver.Template, args any) T {
	te.t.Helper()
	return resolver.ExecOK[T, *ent.Client](te.TestEnvironment, ctx, tpl, args)
}

// execErr executes a GraphQL query and asserts an error containing the message.
func execErr(te *testEnv, ctx context.Context, tpl resolver.Template, args any, wantErrContains string) {
	te.t.Helper()
	resolver.ExecErr[*ent.Client](te.TestEnvironment, ctx, tpl, args, wantErrContains)
}

// =============================================================================
// ENTITY BUILDERS
// =============================================================================

type fileBuilder struct {
	te          *testEnv
	ctx         context.Context //nolint:containedctx // Builder pattern for tests
	user        *authn.User
	refID       uuid.UUID
	refType     file.Reftype
	name        string
	size        int64
	contentType string
	description string
	dataTypeID  uuid.UUID
	data        map[string]any
	publicAlias *string
	deleted     bool
}

func (te *testEnv) newFile(ctx context.Context, user *authn.User) *fileBuilder {
	return &fileBuilder{
		te:          te,
		ctx:         ctx,
		user:        user,
		refID:       uuidgql.GenerateV7UUID(),
		refType:     testRefType,
		name:        "file-" + uuidgql.GenerateV7UUID().String()[:8] + ".txt",
		size:        100,
		contentType: "text/plain",
		description: "test file",
	}
}

func (b *fileBuilder) RefID(id uuid.UUID) *fileBuilder {
	b.refID = id
	return b
}

func (b *fileBuilder) RefType(rt file.Reftype) *fileBuilder {
	b.refType = rt
	return b
}

func (b *fileBuilder) Name(name string) *fileBuilder {
	b.name = name
	return b
}

func (b *fileBuilder) Size(size int64) *fileBuilder {
	b.size = size
	return b
}

func (b *fileBuilder) ContentType(ct string) *fileBuilder {
	b.contentType = ct
	return b
}

func (b *fileBuilder) Description(desc string) *fileBuilder {
	b.description = desc
	return b
}

func (b *fileBuilder) DataTypeID(id uuid.UUID) *fileBuilder {
	b.dataTypeID = id
	return b
}

func (b *fileBuilder) Data(data map[string]any) *fileBuilder {
	b.data = data
	return b
}

func (b *fileBuilder) Alias(alias string) *fileBuilder {
	b.publicAlias = &alias
	return b
}

func (b *fileBuilder) Deleted() *fileBuilder {
	b.deleted = true
	return b
}

func (b *fileBuilder) Create() *ent.File {
	b.te.t.Helper()
	var f *ent.File
	err := b.te.withTx(b.ctx, func(tx *ent.Tx) error {
		builder := tx.File.Create().
			SetTenantID(b.user.TenantID).
			SetRefid(b.refID).
			SetReftype(b.refType).
			SetName(b.name).
			SetSize(b.size).
			SetContentType(b.contentType).
			SetDescription(b.description)

		if b.dataTypeID != uuid.Nil {
			builder.SetDataTypeID(b.dataTypeID)
		}
		if b.data != nil {
			builder.SetData(b.data)
		}
		if b.publicAlias != nil {
			builder.SetPublicAlias(*b.publicAlias)
		}

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		f, err = builder.Save(ent.NewTxContext(b.ctx, tx))
		return err
	})
	require.NoError(b.te.t, err)
	return f
}

func (te *testEnv) withTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	tx, err := te.Ent.Tx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// =============================================================================
// EVENT ASSERTIONS
// =============================================================================

// Event represents an expected event.
type Event struct {
	Entity string
	ID     uuid.UUID
	Op     string
}

func Create(entity string, id uuid.UUID) Event { return Event{entity, id, "create"} }
func Update(entity string, id uuid.UUID) Event { return Event{entity, id, "update"} }
func Delete(entity string, id uuid.UUID) Event { return Event{entity, id, "delete"} }

// clearEvents removes all events from the outbox (call after setup, before action).
func (te *testEnv) clearEvents(ctx context.Context) {
	te.t.Helper()
	_, err := te.Ent.EntityEventsOutbox.Delete().Exec(ctx)
	require.NoError(te.t, err)
}

// assertEvents verifies the outbox contains exactly the expected events.
func (te *testEnv) assertEvents(ctx context.Context, expected ...Event) {
	te.t.Helper()

	entries, err := te.Ent.EntityEventsOutbox.Query().
		Order(ent.Asc(entityeventsoutbox.FieldCreatedAt)).
		All(ctx)
	require.NoError(te.t, err)

	if len(expected) == 0 {
		assert.Empty(te.t, entries, "expected no events, got %d", len(entries))
		return
	}

	require.Len(te.t, entries, len(expected), "event count mismatch")

	for i, want := range expected {
		suffix := "." + want.Entity + "." + want.ID.String() + "." + want.Op
		assert.True(te.t, strings.HasSuffix(entries[i].Topic, suffix),
			"event[%d]: expected topic ending with %q, got %q", i, suffix, entries[i].Topic)
	}
}

// assertNoEvents verifies no events were emitted.
func (te *testEnv) assertNoEvents(ctx context.Context) {
	te.t.Helper()
	te.assertEvents(ctx)
}
