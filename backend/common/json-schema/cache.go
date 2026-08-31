package json_schema

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/memkv"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/std"
)

var (
	ErrDataTypeNotFound    = errors.New("datatype not found")
	ErrUserContextNotFound = errors.New("user context not found")
)

const slugCacheKey = "%s/%s"

type (
	// DataTypesClient retrieves data types from an external source.
	// This interface exists to break the circular dependency between common
	// and management: management depends on common, so common cannot import
	// management/api directly. The implementation lives in
	// management/pkg/datatypes.
	DataTypesClient interface {
		// GetDataTypes returns every non-deleted DataType across all tenants,
		// ordered by version ascending so the highest version of any
		// (tenant, slug) pair lands in the cache last during initial load.
		// (Update also tiebreaks on version, so load order is belt-and-
		// suspenders, not a correctness dependency.)
		GetDataTypes(ctx context.Context) ([]DataType, error)
		// GetDataTypeBySlug returns the latest non-deleted DataType for the
		// given (tenantID, slug). The tenant is passed explicitly because the
		// caller (the NATS-driven Delete promotion) runs under a system token
		// with no request-scoped tenant — without it the management query skips
		// its tenant filter and resolves the global highest-version row for the
		// slug, which can belong to another tenant. Returns ErrDataTypeNotFound
		// when no version exists.
		GetDataTypeBySlug(ctx context.Context, slug string, tenantID uuid.UUID) (*DataType, error)
		// GetDataTypeByID returns the DataType for the given (tenantID, id).
		// The tenant is passed explicitly for the same reason as in
		// GetDataTypeBySlug: downstream services call management with a
		// system token, and management's tenant privacy filter SKIPS system
		// users — an id-only query would resolve (and the cache would then
		// store) another tenant's row. Returns ErrDataTypeNotFound when the
		// row is missing or soft-deleted. Used as the cache-miss fallback so
		// a fresh `createDataType` → `createItem` race against NATS event
		// propagation can still succeed.
		GetDataTypeByID(ctx context.Context, id uuid.UUID, tenantID uuid.UUID) (*DataType, error)
	}

	// DataTypesCacheOptions configures a DataTypesCache instance.
	DataTypesCacheOptions struct {
		Fetcher     DataTypesClient
		Consumer    *nats.ConsumerInfo
		Stream      string
		Topics      []string
		ServiceName string
		// OnUpdate, when set, is called after a create/update event has been
		// applied to the cache. It runs on the consumer goroutine, so an
		// implementation that does real work must hand it off. Optional: a
		// service that only reads schemas leaves it nil.
		OnUpdate func(dt DataType)
	}

	// DataTypesCache is an in-memory cache of data type definitions fetched from
	// the management service.
	//
	// Indexed by both ID (versioned, immutable lookup) and slug (newest version
	// per tenant). Slug→ID lookup is best-effort, refreshed by NATS events on
	// create/delete, and only used by the legacy slug-addressed path — entity
	// writes always carry data_type_id explicitly under #990.
	DataTypesCache struct {
		fetcher     DataTypesClient
		serviceName string
		memStore    *memkv.InMemoryKVStore
		consumer    jetstream.Consumer
		onUpdate    func(dt DataType)
	}

	// DataType represents a cached data type definition. It is stored in the
	// in-memory KV store keyed by both ID and slug (scoped to tenant).
	DataType struct {
		ID         uuid.UUID  `json:"id"`
		Name       string     `json:"name"`
		Slug       string     `json:"slug"`
		TenantID   uuid.UUID  `json:"tenant_id"`
		JsonSchema string     `json:"json_schema"`
		Version    int        `json:"version"`
		CreatedAt  time.Time  `json:"created_at"`
		DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	}
)

// NewDataTypesCache returns a DataTypesCache instance. It creates a NATS
// JetStream consumer for real-time data type events (unless a pre-existing
// Consumer is provided in options).
func NewDataTypesCache(ctx context.Context, js jetstream.JetStream, options DataTypesCacheOptions) (*DataTypesCache, error) {
	logger := log.ForContext(ctx)

	var cons jetstream.Consumer
	var err error
	if options.Consumer == nil {
		cons, err = js.CreateOrUpdateConsumer(ctx, options.Stream, jetstream.ConsumerConfig{
			Name:              options.ServiceName + "DataType",
			FilterSubjects:    options.Topics,
			InactiveThreshold: 10 * time.Minute,
		})
		if err != nil {
			logger.Err(err).Msg("creating consumer")
			return nil, err
		}
		logger.Info().Str("consumerName", options.ServiceName+"DataType").Msg("Consumer created")
	}

	return &DataTypesCache{
		fetcher:     options.Fetcher,
		memStore:    memkv.NewInMemoryKVStore(0),
		consumer:    cons,
		serviceName: options.ServiceName,
		onUpdate:    options.OnUpdate,
	}, nil
}

// ListenToEvents listens to the datatype topics and adds, updates and removes
// data types from the local memory cache. It blocks until ctx is cancelled,
// then makes a best-effort, time-bounded attempt to drain the consumer.
// Nothing waits on this function returning, so that drain is not a
// guarantee: the actual guarantee that in-flight messages complete comes
// from the connection-level drain (DrainNatsClient), which drains every
// subscription before the connection closes.
func (dc *DataTypesCache) ListenToEvents(ctx context.Context) {
	logger := log.ForContext(ctx)

	cc, err := dc.consumer.Consume(func(msg jetstream.Msg) {
		msgCtx := events.ContextFromJetstreamMessage(ctx, msg)
		logger := log.ForContext(msgCtx)

		payload, err := std.UnmarshalJson[events.MutationEventMessage](msg.Data())
		if err != nil {
			// Ack a payload that cannot be decoded: redelivery would fail
			// identically forever, and the consumer has no MaxDeliver bound.
			logger.Err(err).Str("service", dc.serviceName).Str("payload", string(msg.Data())).
				Msg("Datatypes event consumer: discarding undecodable message")
			if ackErr := msg.Ack(); ackErr != nil {
				logger.Err(ackErr).Str("service", dc.serviceName).Msg("Error nats ack")
			}
			return
		}

		switch payload.Operation {
		case "create", "update":
			// DataType is append-only: a create is a new version of a
			// (tenant, slug) family, an update only renames (`name` is the
			// one mutable field). Both upsert the cached row; Update's
			// version tiebreak keeps a slug slot that points at a newer
			// version untouched while the ID slot (and a same-version slug
			// slot) picks up the change.
			payloadData, ok := dataTypeFromEvent(payload)
			if !ok {
				logger.Error().Str("service", dc.serviceName).Str("operation", payload.Operation).
					Str("id", payload.ID.String()).Msg("Event payload data is nil or not a map")
				break
			}

			dc.Update(payload.ID, payloadData)
			// Fires on create as well: a changed binding set arrives as a
			// new version, never as an in-place update, so a consumer that
			// only listened to updates would never see it.
			if dc.onUpdate != nil {
				dc.onUpdate(payloadData)
			}
		case "delete":
			// Carry the slug from the event payload: the row is already
			// soft-deleted in management, so re-reading it by ID would return
			// NotFound and leave the slug index stale (see DataTypesCache.Delete).
			payloadData, _ := dataTypeFromEvent(payload)
			dc.Delete(msgCtx, payloadData)
		default:
			logger.Warn().Str("operation", payload.Operation).Msg("operation is unknown")
		}
		logger.Info().Str("data_type_id", payload.ID.String()).Str("operation", payload.Operation).Msg("DataTypeEvent processed")
		err = msg.Ack()
		if err != nil {
			logger.Err(err).Str("service", dc.serviceName).Msg("Error nats ack")
		}
	})
	if err != nil {
		logger.Err(err).Msg("Error consuming datatypes")
		return
	}

	<-ctx.Done()
	cc.Drain()
	select {
	case <-cc.Closed():
		logger.Info().Msg("datatypes consumer drained")
	case <-time.After(events.DrainTimeout):
		// Reached when the connection was closed under us before the
		// subscription drain finished (nats.go checkDrained bails on
		// nc.IsClosed() without firing the closed handler), so Closed()
		// would never fire. Nothing waits on this goroutine, so the only
		// cost of the timeout expiring is this log line.
		logger.Warn().Dur("timeout", events.DrainTimeout).
			Msg("datatypes consumer drain timed out; the connection drain covers any remaining messages")
	}
}

// visibleToCaller reports whether dt belongs to the tenant the request acts
// on. The cache holds every tenant's rows (it subscribes to all tenants'
// events), so a hit must be tenant-checked: a caller reaching the cache
// directly rather than through the validator would otherwise resolve a
// foreign row by id. A zero tenant only occurs in tenant-agnostic test
// fixtures (the column is NOT NULL), and a context without a single acting
// tenant leaves the check to the caller's own guard.
func visibleToCaller(ctx context.Context, dt DataType) bool {
	if dt.TenantID == uuid.Nil {
		return true
	}
	req := request.ForContext(ctx)
	return !req.HasMutationTenantID() || dt.TenantID == req.MutationTenantID()
}

// ReadByID returns the cached DataType for id. On cache miss it falls back
// to fetching from management via the configured fetcher and populates the
// cache on success. Returns ErrDataTypeNotFound when management has no live
// row for the id, and for an id this process has seen deleted.
func (dc *DataTypesCache) ReadByID(ctx context.Context, id uuid.UUID) (*DataType, error) {
	if val, ok := dc.memStore.Get(id.String()); ok {
		if dt, isDataType := val.(DataType); isDataType {
			if dt.DeletedAt != nil || !visibleToCaller(ctx, dt) {
				return nil, ErrDataTypeNotFound
			}
			return &dt, nil
		}
	}

	// Cache miss — try the management fetcher to cover the race where a
	// fresh DataType hasn't propagated via NATS yet. Skip the fallback if
	// no fetcher is wired (e.g., unit tests using mock validators that
	// inject their own DataTypes directly) or the context carries no
	// unambiguous tenant to scope the fetch by (the fallback only matters
	// inside entity mutations, which always have one). Like ReadBySlug, the
	// tenant comes from the context; ReadBySlug's TODO about moving it into
	// the signature applies here too.
	req := request.ForContext(ctx)
	if dc.fetcher == nil || !req.HasMutationTenantID() || req.MutationTenantID() == uuid.Nil {
		return nil, ErrDataTypeNotFound
	}
	fetched, err := dc.fetcher.GetDataTypeByID(ctx, id, req.MutationTenantID())
	if err != nil {
		return nil, err
	}
	if fetched == nil || fetched.DeletedAt != nil {
		return nil, ErrDataTypeNotFound
	}
	dc.Update(id, *fetched)
	return fetched, nil
}

func (dc *DataTypesCache) ReadBySlug(ctx context.Context, slug string) (*DataType, error) {
	req := request.ForContext(ctx)
	if !req.User().IsAuthenticated() {
		return nil, ErrUserContextNotFound
	}

	// TODO(michael): The tenant IDs should be part of the function signature.
	// Directly accessing the context here means we have to make assumptions
	// which operation is being performed and in which context. This is prone to
	// errors and unnecessarily hard to test. This function is indirectly called
	// from the create/update mutations, which already know exactly which
	// TenantID they operate on.
	tenantID := req.MutationTenantID()

	val, ok := dc.memStore.Get(dc.getSlugCacheKey(slug, &tenantID))
	if !ok {
		return nil, ErrDataTypeNotFound
	}

	dt, ok := val.(DataType)
	if !ok {
		return nil, ErrDataTypeNotFound
	}

	return &dt, nil
}

// dataTypeFromEvent extracts the DataType carried in a mutation event payload.
// It prefers DataAfter (the post-mutation row) and falls back to DataBefore,
// always overriding ID/TenantID with the authoritative envelope values. ok is
// false when neither field decodes to a row (slug unknown) — delete callers
// treat that as a best-effort, ID-only result.
func dataTypeFromEvent(payload events.MutationEventMessage) (DataType, bool) {
	// Prefer data_after, but treat ANY non-map value as absent, not only
	// null: a publisher-side builder mixup can put a scalar there (a bulk
	// mutation returns its affected-row count), and for a delete event
	// data_before still carries the slug that promotion needs.
	dataMap, ok := payload.DataAfter.(map[string]interface{})
	if !ok {
		dataMap, ok = payload.DataBefore.(map[string]interface{})
	}
	if !ok {
		return DataType{ID: payload.ID, TenantID: payload.TenantID}, false
	}
	dt, err := std.MapToStruct[DataType](dataMap)
	if err != nil {
		return DataType{ID: payload.ID, TenantID: payload.TenantID}, false
	}
	dt.ID = payload.ID
	if dt.TenantID == uuid.Nil {
		dt.TenantID = payload.TenantID
	}
	// The event carries the ent row, whose deleted_at is a plain time.Time:
	// a live row serialises as the zero time, not null. Decoded into the
	// pointer here that reads as "deleted", and Update would drop every
	// create/update event on the floor -- leaving the slug slot on whatever
	// version a cache-miss fetch last happened to load. Same sentinel rule
	// as every other consumer of these payloads.
	if !topic.IsDeletedAt(dataMap["deleted_at"]) {
		dt.DeletedAt = nil
	}
	return dt, true
}

// Update stores dt in the cache, indexed by both ID and slug (per tenant).
//
// Defense in depth: soft-deleted rows are skipped even though management's
// event stream is supposed to filter them out — the cache is the last line
// before resolver writes touch entities, so we err on the strict side.
//
// Slug-index ordering: if the slot already holds a HIGHER version for the
// same (slug, tenant), the slug write is suppressed. The ID slot always wins
// (one-to-one with the row). Comparing the monotonic version integer handles
// out-of-order JetStream delivery across distinct subjects when two versions
// of the same slug are created in quick succession.
func (dc *DataTypesCache) Update(id uuid.UUID, dt DataType) {
	if dt.DeletedAt != nil {
		return
	}

	// A tombstoned ID slot wins: deletion is one-way for an immutable
	// version, so any event still carrying this ID describes the row's
	// pre-delete state. JetStream redelivers a message whose Ack failed, and
	// the ReadByID fallback can return a row fetched before the delete
	// committed — without this guard either would reinstall a deleted
	// version as live, permanently (no later event corrects it).
	if !dc.memStore.SetWhere(id.String(), dt, 0, func(existing any, exists bool) bool {
		if !exists {
			return true
		}
		cur, ok := existing.(DataType)
		return !ok || cur.DeletedAt == nil
	}) {
		return
	}

	// Suppress the slug write when the slot already holds a HIGHER version
	// for the same (slug, tenant). Predicate and write share one lock
	// acquisition: the NATS consumer and a ReadByID fallback both call this,
	// and a get-then-set pair lets the lower version land last.
	dc.memStore.SetWhere(dc.getSlugCacheKey(dt.Slug, &dt.TenantID), dt, 0, func(existing any, exists bool) bool {
		if !exists {
			return true
		}
		cur, ok := existing.(DataType)
		return !ok || cur.Version <= dt.Version
	})
}

// Delete removes the datatype carried by dt (identified by dt.ID) from the
// local memory cache.
//
// dt.Slug and dt.TenantID come from the delete event payload rather than a
// re-read: by the time a delete event fires, the row is soft-deleted in
// management, so GetDataTypeByID would return NotFound and the slug index
// could never be corrected on a cold cache.
//
// If the deleted row was pointed at by its slug index, the slug entry is
// either re-pointed at the next-newest non-deleted version (fetched from
// management) or dropped entirely if no other version exists.
func (dc *DataTypesCache) Delete(ctx context.Context, dt DataType) {
	logger := log.ForContext(ctx)

	// Tombstone rather than drop: an empty slot would be refilled by a
	// redelivered create event or an in-flight ReadByID fetch, resurrecting
	// the version as live. The tombstone also answers later reads without a
	// management round-trip (soft-deleted rows are filtered server-side, so
	// the fetch could only confirm the deletion).
	dc.memStore.Set(dt.ID.String(), tombstoneOf(dt), 0)

	// Without a slug from the payload we cannot locate the slug index entry.
	// The ID slot is tombstoned, so writes pinned by id are already
	// rejected; a slug-addressed read keeps resolving the deleted row until
	// a new version of that slug arrives or the process restarts.
	if dt.Slug == "" {
		logger.Warn().Str("service", dc.serviceName).Str("id", dt.ID.String()).
			Msg("DataType cache: delete event carried no slug; slug index left stale")
		return
	}

	slugKey := dc.getSlugCacheKey(dt.Slug, &dt.TenantID)

	// If the slug index already points at a NEWER version, leave it alone —
	// the deleted row wasn't the "latest" anymore. A lower-versioned foreign
	// occupant is stale (leftover from a lost earlier delete event, since the
	// just-deleted row outversioned it) and falls through to be replaced.
	if existing, ok := dc.memStore.Get(slugKey); ok {
		if cur, ok := existing.(DataType); ok && cur.ID != dt.ID && cur.Version > dt.Version {
			return
		}
	}

	// Slug index pointed at the just-deleted row. Fetch the next-newest
	// non-deleted version FIRST and only then touch the slot: DataType is
	// append-only, so for a stable slug no later create event would ever
	// repopulate a slot emptied by a delete-then-fetch order — a single
	// failed round-trip would be a permanent per-process cache hole.
	var next *DataType
	if dc.fetcher != nil {
		fetched, err := dc.fetcher.GetDataTypeBySlug(ctx, dt.Slug, dt.TenantID)
		if err != nil && !errors.Is(err, ErrDataTypeNotFound) {
			// Transient failure (timeout, restart): keep the entry, but stamp
			// it as deleted so slug-addressed writes fail with
			// ErrDataTypeDeleted (the correct semantics for a tombstoned
			// version) instead of validating against a live-looking copy of
			// the deleted row until the process restarts.
			logger.Err(err).Str("service", dc.serviceName).Str("slug", dt.Slug).
				Msg("DataType cache: failed to fetch successor on delete; marking slug entry deleted")
			dc.tombstoneSlugSlot(slugKey, dt)
			return
		}
		if err == nil {
			next = fetched
		}
	}
	// Remove the dead occupant atomically, then promote through Update so
	// its version tiebreak applies — a create event that landed while the
	// fetch was in flight carries a HIGHER version and must survive both the
	// delete and the promotion of an older fetched successor.
	dc.deleteSlugSlotUnlessNewer(slugKey, dt)
	if next != nil && next.DeletedAt == nil {
		dc.Update(next.ID, *next)
	}
}

// deleteSlugSlotUnlessNewer atomically removes the slug index entry unless a
// different row with a HIGHER version occupies it — such an occupant comes
// from a concurrent create event and must survive. Occupants at or below the
// deleted row's version are stale (the deleted row itself, or a leftover
// from a lost earlier delete event) and are dropped with it.
func (dc *DataTypesCache) deleteSlugSlotUnlessNewer(slugKey string, deleted DataType) {
	dc.memStore.DeleteWhere(func(key string, value any) bool {
		if key != slugKey {
			return false
		}
		cur, ok := value.(DataType)
		return !ok || cur.ID == deleted.ID || cur.Version <= deleted.Version
	})
}

// tombstoneSlugSlot re-stamps the slug entry with the delete event's
// tombstone while it still points at the deleted row, so readers see a
// deleted DataType rather than a live-looking stale copy.
func (dc *DataTypesCache) tombstoneSlugSlot(slugKey string, dt DataType) {
	// Check and write under one lock acquisition so a concurrent promotion
	// of a newer version cannot be overwritten by this tombstone.
	dc.memStore.SetWhere(slugKey, tombstoneOf(dt), 0, func(existing any, exists bool) bool {
		if !exists {
			return false
		}
		cur, ok := existing.(DataType)
		return ok && cur.ID == dt.ID
	})
}

// tombstoneOf returns dt marked deleted, stamping the current time when the
// event payload carried no deletion timestamp.
func tombstoneOf(dt DataType) DataType {
	if dt.DeletedAt == nil {
		now := time.Now().UTC()
		dt.DeletedAt = &now
	}
	return dt
}

// RetrieveJsonSchemasToCache loads all datatype schemas from the management
// service into the local memory cache, and returns them so a caller that needs
// the whole set at boot does not have to fetch it again.
func (dc *DataTypesCache) RetrieveJsonSchemasToCache(ctx context.Context) ([]DataType, error) {
	logger := log.ForContext(ctx)

	dataTypes, err := dc.fetcher.GetDataTypes(ctx)
	if err != nil {
		return nil, err
	}

	logger.Info().Msg("Adding schemas to memory...")
	for _, dt := range dataTypes {
		dc.Update(dt.ID, dt)
	}
	logger.Info().Int("count", len(dataTypes)).Msg("Schemas successfully added to memory")

	return dataTypes, nil
}

func (dc *DataTypesCache) getSlugCacheKey(slug string, tenantID *uuid.UUID) string {
	return fmt.Sprintf(slugCacheKey, slug, tenantID.String())
}
