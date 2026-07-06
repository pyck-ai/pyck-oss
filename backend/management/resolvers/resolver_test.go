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

	_ "github.com/mattn/go-sqlite3"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"
	"github.com/pyck-ai/pyck/backend/common/uuidgql"
	"github.com/pyck-ai/pyck/backend/common/validator"

	"github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/entityeventsoutbox"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/enttest"
	"github.com/pyck-ai/pyck/backend/management/resolvers"
)

// =============================================================================
// TEST SETUP
// =============================================================================

func TestMain(m *testing.M) {
	cleanup := resolver.MustSetupTestTracer("management")
	defer cleanup()
	os.Exit(m.Run())
}

// =============================================================================
// FIXTURES
// =============================================================================

var (
	userA = &authn.User{
		ID:       uuid.MustParse("54ef017a-d8cc-4b1a-8495-da867b84059d"),
		TenantID: resolver.TenantA,
		Roles:    map[uuid.UUID]authn.Role{resolver.TenantA: authn.ROLE_ADMIN},
	}
	userAWriter = &authn.User{
		ID:       uuid.MustParse("c2b1f0a3-4e5f-4b6a-8c7d-9e8f0a1b2c35"),
		TenantID: resolver.TenantA,
		Roles:    map[uuid.UUID]authn.Role{resolver.TenantA: authn.ROLE_WRITER},
	}
	userB = &authn.User{
		ID:       uuid.MustParse("af3ae7b9-959a-4521-a3a3-66a0efa7844a"),
		TenantID: resolver.TenantB,
		Roles:    map[uuid.UUID]authn.Role{resolver.TenantB: authn.ROLE_ADMIN},
	}
	userBReader = &authn.User{
		ID:       uuid.MustParse("d1c8f0b2-3e4f-4a5b-9c6d-7e8f9a0b1c2d"),
		TenantID: resolver.TenantB,
		Roles:    map[uuid.UUID]authn.Role{resolver.TenantB: authn.ROLE_READER},
	}

	systemUser = authn.SystemUser()

	testDataTypeSlug   = "item"
	testDataTypeSchema = string(test.MustLoadSchemaByName(testDataTypeSlug))
)

// =============================================================================
// TEST ENVIRONMENT
// =============================================================================

type testEnv struct {
	*resolver.TestEnvironment[*gen.Client]
	t *testing.T
}

func setup(t *testing.T) *testEnv {
	t.Helper()

	te := &testEnv{
		TestEnvironment: resolver.NewTestEnvironment[*gen.Client](t),
		t:               t,
	}

	client := enttest.Open(t, dialect.SQLite, resolver.DatabaseURI(t),
		enttest.WithOptions(gen.Log(t.Log)),
	).Debug()

	client.Use(events.MutationEventHook(events.HookConfig{
		Service:        "management",
		StreamName:     "pyck",
		EntityFetcher:  events.BuildEntityFetcher(gen.TxFromContext, events.FieldData),
		OutboxInserter: events.NewEntOutboxInserter(gen.TxFromContext),
	}))

	v := validator.NewValidator(te.DataTypeProvider)
	r := resolvers.NewResolver("management", client, v, te.WorkflowClient, nil)
	schema := resolvers.NewSchema(r)

	te.Init(client, schema, func(s *handler.Server) {
		s.Use(gqltx.NewMiddleware(client, gen.NewTxContext, "management-test", 0))
	})

	return te
}

// ctx creates a request context for the given user.
func (te *testEnv) ctx(user *authn.User) context.Context {
	te.t.Helper()
	return te.ctxForTenant(user, user.TenantID)
}

// ctxForTenant creates a request context with MutationTenantID set to
// the supplied tenantID instead of the user's own TenantID. Used by
// tenant-lifecycle tests where the test tenant ID is generated fresh
// per-subtest and the caller must target that specific tenant.
func (te *testEnv) ctxForTenant(user *authn.User, tenantID uuid.UUID) context.Context {
	te.t.Helper()
	ctx := request.Context(te.t.Context(), user, tenantID)
	tracer := otelapi.Tracer("management-test")
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
	return resolver.ExecOK[T, *gen.Client](te.TestEnvironment, ctx, tpl, args)
}

// execErr executes a GraphQL query and asserts an error containing the message.
func execErr(te *testEnv, ctx context.Context, tpl resolver.Template, args any, wantErrContains string) {
	te.t.Helper()
	resolver.ExecErr[*gen.Client](te.TestEnvironment, ctx, tpl, args, wantErrContains)
}

// =============================================================================
// ENTITY BUILDERS
// =============================================================================

// --- Location Builder ---

type locationBuilder struct {
	te      *testEnv
	ctx     context.Context //nolint:containedctx // Builder pattern for tests
	user    *authn.User
	name    string
	data    map[string]any
	deleted bool
}

func (te *testEnv) newLocation(ctx context.Context, user *authn.User) *locationBuilder {
	return &locationBuilder{
		te:   te,
		ctx:  ctx,
		user: user,
		name: "Location-" + uuidgql.GenerateV7UUID().String(),
	}
}

func (b *locationBuilder) Name(name string) *locationBuilder {
	b.name = name
	return b
}

func (b *locationBuilder) Data(data map[string]any) *locationBuilder {
	b.data = data
	return b
}

func (b *locationBuilder) Deleted() *locationBuilder {
	b.deleted = true
	return b
}

func (b *locationBuilder) Create() *gen.Location {
	b.te.t.Helper()
	var loc *gen.Location
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		builder := tx.Location.Create().
			SetTenantID(b.user.TenantID).
			SetName(b.name)

		if b.data != nil {
			builder.SetData(b.data)
		}

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		loc, err = builder.Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return loc
}

// --- Device Builder ---

type deviceBuilder struct {
	te      *testEnv
	ctx     context.Context //nolint:containedctx // Builder pattern for tests
	user    *authn.User
	name    string
	data    map[string]any
	deleted bool
}

func (te *testEnv) newDevice(ctx context.Context, user *authn.User) *deviceBuilder {
	return &deviceBuilder{
		te:   te,
		ctx:  ctx,
		user: user,
		name: "Device-" + uuidgql.GenerateV7UUID().String(),
	}
}

func (b *deviceBuilder) Name(name string) *deviceBuilder {
	b.name = name
	return b
}

func (b *deviceBuilder) Data(data map[string]any) *deviceBuilder {
	b.data = data
	return b
}

func (b *deviceBuilder) Deleted() *deviceBuilder {
	b.deleted = true
	return b
}

func (b *deviceBuilder) Create() *gen.Device {
	b.te.t.Helper()
	var dev *gen.Device
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		builder := tx.Device.Create().
			SetTenantID(b.user.TenantID).
			SetName(b.name)

		if b.data != nil {
			builder.SetData(b.data)
		}

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		dev, err = builder.Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return dev
}

// --- DeviceLocation Builder ---

type deviceLocationBuilder struct {
	te         *testEnv
	ctx        context.Context //nolint:containedctx // Builder pattern for tests
	user       *authn.User
	deviceID   uuid.UUID
	locationID uuid.UUID
	data       map[string]any
	deleted    bool
}

func (te *testEnv) newDeviceLocation(ctx context.Context, user *authn.User, deviceID, locationID uuid.UUID) *deviceLocationBuilder {
	return &deviceLocationBuilder{
		te:         te,
		ctx:        ctx,
		user:       user,
		deviceID:   deviceID,
		locationID: locationID,
	}
}

func (b *deviceLocationBuilder) Data(data map[string]any) *deviceLocationBuilder {
	b.data = data
	return b
}

func (b *deviceLocationBuilder) Deleted() *deviceLocationBuilder {
	b.deleted = true
	return b
}

func (b *deviceLocationBuilder) Create() *gen.DeviceLocation {
	b.te.t.Helper()
	var dl *gen.DeviceLocation
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		builder := tx.DeviceLocation.Create().
			SetTenantID(b.user.TenantID).
			SetDeviceID(b.deviceID).
			SetLocationID(b.locationID)

		if b.data != nil {
			builder.SetData(b.data)
		}

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		dl, err = builder.Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return dl
}

// --- User Builder ---

type userBuilder struct {
	te        *testEnv
	ctx       context.Context //nolint:containedctx // Builder pattern for tests
	user      *authn.User
	userID    uuid.UUID
	username  string
	email     string
	firstName string
	lastName  string
	deleted   bool
}

func (te *testEnv) newUser(ctx context.Context, user *authn.User) *userBuilder {
	id := uuidgql.GenerateV7UUID()
	return &userBuilder{
		te:        te,
		ctx:       ctx,
		user:      user,
		userID:    id,
		username:  "user-" + id.String()[:8],
		email:     "user-" + id.String()[:8] + "@test.com",
		firstName: "First",
		lastName:  "Last",
	}
}

func (b *userBuilder) ID(id uuid.UUID) *userBuilder {
	b.userID = id
	return b
}

func (b *userBuilder) Username(username string) *userBuilder {
	b.username = username
	return b
}

func (b *userBuilder) Email(email string) *userBuilder {
	b.email = email
	return b
}

func (b *userBuilder) Deleted() *userBuilder {
	b.deleted = true
	return b
}

func (b *userBuilder) Create() *gen.User {
	b.te.t.Helper()
	var usr *gen.User
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		builder := tx.User.Create().
			SetID(b.userID).
			SetIdpID(b.userID.String()).
			SetTenantID(b.user.TenantID).
			SetUsername(b.username).
			SetFirstName(b.firstName).
			SetLastName(b.lastName).
			SetEmail(b.email)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		usr, err = builder.Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return usr
}

// --- DeviceUser Builder ---

type deviceUserBuilder struct {
	te       *testEnv
	ctx      context.Context //nolint:containedctx // Builder pattern for tests
	user     *authn.User
	deviceID uuid.UUID
	userID   uuid.UUID
	deleted  bool
}

func (te *testEnv) newDeviceUser(ctx context.Context, user *authn.User, deviceID, userID uuid.UUID) *deviceUserBuilder {
	return &deviceUserBuilder{
		te:       te,
		ctx:      ctx,
		user:     user,
		deviceID: deviceID,
		userID:   userID,
	}
}

func (b *deviceUserBuilder) Deleted() *deviceUserBuilder {
	b.deleted = true
	return b
}

func (b *deviceUserBuilder) Create() *gen.DeviceUser {
	b.te.t.Helper()
	var du *gen.DeviceUser
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		builder := tx.DeviceUser.Create().
			SetTenantID(b.user.TenantID).
			SetDeviceID(b.deviceID).
			SetUserID(b.userID)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		du, err = builder.Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return du
}

// --- Tenant Builder ---

type tenantBuilder struct {
	te        *testEnv
	ctx       context.Context //nolint:containedctx // Builder pattern for tests
	tenantID  uuid.UUID
	name      string
	expiresAt *time.Time
	deleted   bool
	data      map[string]any
}

func (te *testEnv) newTenant(ctx context.Context, tenantID uuid.UUID) *tenantBuilder {
	return &tenantBuilder{
		te:       te,
		ctx:      ctx,
		tenantID: tenantID,
		name:     tenantID.String(),
	}
}

func (b *tenantBuilder) Name(name string) *tenantBuilder {
	b.name = name
	return b
}

func (b *tenantBuilder) ExpiresAt(t time.Time) *tenantBuilder {
	utc := t.UTC()
	b.expiresAt = &utc
	return b
}

func (b *tenantBuilder) Deleted() *tenantBuilder {
	b.deleted = true
	return b
}

func (b *tenantBuilder) Data(data map[string]any) *tenantBuilder {
	b.data = data
	return b
}

func (b *tenantBuilder) Create() *gen.Tenant {
	b.te.t.Helper()
	var tenant *gen.Tenant
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		builder := tx.Tenant.Create().
			SetID(b.tenantID).
			SetName(b.name).
			SetIdpOrgRef(b.tenantID.String())

		if b.expiresAt != nil {
			builder = builder.SetExpiresAt(*b.expiresAt)
		}
		if b.deleted {
			builder = builder.SetDeletedAt(time.Now().UTC()).SetDeletedBy(uuid.Max)
		}
		if b.data != nil {
			builder = builder.SetData(b.data)
		}

		var err error
		tenant, err = builder.Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return tenant
}

// --- Event Builder ---

type eventBuilder struct {
	te          *testEnv
	ctx         context.Context //nolint:containedctx // Builder pattern for tests
	name        string
	description string
	topic       string
	example     map[string]any
}

func (te *testEnv) newEvent(ctx context.Context) *eventBuilder {
	return &eventBuilder{
		te:          te,
		ctx:         ctx,
		name:        "Event-" + uuidgql.GenerateV7UUID().String()[:8],
		description: "Test event description",
		topic:       "management-test-topic",
		example:     map[string]any{"test": "data"},
	}
}

func (b *eventBuilder) Name(name string) *eventBuilder {
	b.name = name
	return b
}

func (b *eventBuilder) Description(desc string) *eventBuilder {
	b.description = desc
	return b
}

func (b *eventBuilder) Topic(topic string) *eventBuilder {
	b.topic = topic
	return b
}

func (b *eventBuilder) Example(example map[string]any) *eventBuilder {
	b.example = example
	return b
}

func (b *eventBuilder) Create() *gen.Event {
	b.te.t.Helper()
	var event *gen.Event
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		var err error
		event, err = tx.Event.Create().
			SetName(b.name).
			SetDescription(b.description).
			SetTopic(b.topic).
			SetExample(b.example).
			Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return event
}

// --- DataType Builder ---

type dataTypeBuilder struct {
	te          *testEnv
	ctx         context.Context //nolint:containedctx // Builder pattern for tests
	user        *authn.User
	name        string
	slug        string
	description string
	entity      string
	jsonSchema  string
	deleted     bool
}

func (te *testEnv) newDataType(ctx context.Context, user *authn.User) *dataTypeBuilder {
	slug := "datatype-" + uuidgql.GenerateV7UUID().String()[:8]
	return &dataTypeBuilder{
		te:          te,
		ctx:         ctx,
		user:        user,
		name:        "DataType-" + slug,
		slug:        slug,
		description: "Test data type description",
		entity:      "item",
		jsonSchema:  testDataTypeSchema,
	}
}

func (b *dataTypeBuilder) Name(name string) *dataTypeBuilder {
	b.name = name
	return b
}

func (b *dataTypeBuilder) Slug(slug string) *dataTypeBuilder {
	b.slug = slug
	return b
}

func (b *dataTypeBuilder) Description(desc string) *dataTypeBuilder {
	b.description = desc
	return b
}

func (b *dataTypeBuilder) Entity(entity string) *dataTypeBuilder {
	b.entity = entity
	return b
}

func (b *dataTypeBuilder) JSONSchema(schema string) *dataTypeBuilder {
	b.jsonSchema = schema
	return b
}

func (b *dataTypeBuilder) Deleted() *dataTypeBuilder {
	b.deleted = true
	return b
}

func (b *dataTypeBuilder) Create() *gen.DataType {
	b.te.t.Helper()
	var dt *gen.DataType
	err := b.te.withTx(b.ctx, func(tx *gen.Tx) error {
		builder := tx.DataType.Create().
			SetTenantID(b.user.TenantID).
			SetName(b.name).
			SetSlug(b.slug).
			SetDescription(b.description).
			SetEntity(b.entity).
			SetJSONSchema(b.jsonSchema)

		if b.deleted {
			builder.SetDeletedAt(time.Now()).SetDeletedBy(b.user.ID)
		}

		var err error
		dt, err = builder.Save(gen.NewTxContext(txid.With(b.ctx, txid.New()), tx))
		return err
	})
	require.NoError(b.te.t, err)
	return dt
}

func (te *testEnv) withTx(ctx context.Context, fn func(tx *gen.Tx) error) error {
	// Inject a fresh transaction ID so the MutationEventHook (which now
	// requires one for the outbox row's dedup key) is satisfied — this
	// mirrors what the gqltx middleware does at BeginTx in production.
	ctx = txid.With(ctx, txid.New())
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
		Order(gen.Asc(entityeventsoutbox.FieldCreatedAt)).
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
