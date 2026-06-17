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
	// DataTypesClient retrieves all data types from an external source.
	// This interface exists to break the circular dependency between common
	// and management: management depends on common, so common cannot import
	// management/api directly. The implementation lives in
	// management/pkg/datatypes.
	DataTypesClient interface {
		GetDataTypes(ctx context.Context) ([]DataType, error)
	}

	// DataTypesCacheOptions configures a DataTypesCache instance.
	DataTypesCacheOptions struct {
		Fetcher     DataTypesClient
		Consumer    *nats.ConsumerInfo
		Stream      string
		Topics      []string
		ServiceName string
	}

	// DataTypesCache is an in-memory cache of data type definitions fetched from
	// the management service.
	DataTypesCache struct {
		fetcher     DataTypesClient
		serviceName string
		memStore    *memkv.InMemoryKVStore
		consumer    jetstream.Consumer
	}

	// DataType represents a cached data type definition. It is stored in the
	// in-memory KV store keyed by both ID and slug (scoped to tenant).
	DataType struct {
		ID         uuid.UUID `json:"id"`
		Slug       string    `json:"slug"`
		TenantID   uuid.UUID `json:"tenant_id"`
		JsonSchema string    `json:"json_schema"`
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
	}, nil
}

// ListenToEvents listen to the datatype topic and adds, updates and removes datatypes from the local memory cache.
func (dc *DataTypesCache) ListenToEvents(ctx context.Context) {
	logger := log.ForContext(ctx)

	_, err := dc.consumer.Consume(func(msg jetstream.Msg) {
		msgCtx := events.ContextFromJetstreamMessage(ctx, msg)
		logger := log.ForContext(msgCtx)

		payload, err := std.UnmarshalJson[events.MutationEventMessage](msg.Data())
		if err != nil {
			logger.Err(err).Str("service", dc.serviceName).Str("payload", string(msg.Data())).
				Msg("Datatypes event consumer")
			_ = msg.Nak()
			return
		}

		switch payload.Operation {
		case "create", "update":
			dataMap, ok := payload.DataAfter.(map[string]interface{})
			if !ok {
				logger.Error().Str("service", dc.serviceName).Str("operation", payload.Operation).
					Str("id", payload.ID.String()).Msg("Event payload data is nil or not a map")
				break
			}

			payloadData, err := std.MapToStruct[DataType](dataMap)
			if err != nil {
				logger.Err(err).Str("service", dc.serviceName).Msg("Error mapping data")
				break
			}

			dc.Update(payload.ID, payloadData)
		case "delete":
			dc.Delete(msgCtx, payload.ID)
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
}

func (dc *DataTypesCache) ReadByID(ctx context.Context, id uuid.UUID) (*DataType, error) {
	val, ok := dc.memStore.Get(id.String())
	if !ok {
		return nil, ErrDataTypeNotFound
	}
	dt, ok := val.(DataType)
	if !ok {
		return nil, ErrDataTypeNotFound
	}
	return &dt, nil
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

func (dc *DataTypesCache) Update(id uuid.UUID, dt DataType) {
	// Update by datatype ID
	dc.memStore.Set(id.String(), dt, 0)

	// Update by slug
	dc.memStore.Set(dc.getSlugCacheKey(dt.Slug, &dt.TenantID), dt, 0)
}

// Delete removes a datatype schema with the given id from the local memory cache.
func (dc *DataTypesCache) Delete(ctx context.Context, id uuid.UUID) {
	dt, err := dc.ReadByID(ctx, id)
	if err != nil {
		dc.memStore.Delete(id.String())
		return
	}
	dc.memStore.Delete(id.String())
	dc.memStore.Delete(dc.getSlugCacheKey(dt.Slug, &dt.TenantID))
}

// RetrieveJsonSchemasToCache loads all datatypes schemas over graphql from the management service
// to the local memory cache.
func (dc *DataTypesCache) RetrieveJsonSchemasToCache(ctx context.Context) error {
	logger := log.ForContext(ctx)

	dataTypes, err := dc.fetcher.GetDataTypes(ctx)
	if err != nil {
		return err
	}

	logger.Info().Msg("Adding schemas to memory...")
	for _, dt := range dataTypes {
		dc.Update(dt.ID, dt)
	}
	logger.Info().Int("count", len(dataTypes)).Msg("Schemas successfully added to memory")

	return nil
}

func (dc *DataTypesCache) getSlugCacheKey(slug string, tenantID *uuid.UUID) string {
	return fmt.Sprintf(slugCacheKey, slug, tenantID.String())
}
