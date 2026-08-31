//go:build integration

package dataindex

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	pickingmodel "github.com/pyck-ai/pyck/backend/picking/model"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	// indexVisibleTimeout bounds the whole chain: outbox drain into JetStream,
	// picking's cache applying the datatype, then one backfill pass. Sub-second
	// on a healthy stack.
	indexVisibleTimeout = 60 * time.Second
	pollInterval        = 500 * time.Millisecond

	indexedSerial = "SN-E2E-0001"
	unusedSerial  = "SN-E2E-0002"

	schemaWithoutBinding = `{"type":"object","properties":{"serials":{"type":"array"}}}`
	schemaWithBinding    = `{"type":"object","properties":{"serials":{"type":"array"}},` +
		`"x-indices":{"serialNumbers":{"source":"/serials","slot":"data_ix_list1"}}}`
)

// DataIndexSuite drives one tenant through: a datatype with no binding, an
// order written pinned to it, the binding applied live as a new version of
// the same slug (a DataType is append-only, so a changed binding set is a new
// version, never an in-place edit), and the query that must then find that
// order although it stays pinned to the version without the binding.
type DataIndexSuite struct {
	tests.Base

	pat        string
	slug       string
	dataTypeID uuid.UUID
	orderID    string
	picking    pickingapi.Client
	management managementapi.Client
}

func (s *DataIndexSuite) TestDataIndexLifecycle() {
	r := s.Require()

	if !s.Run("provision tenant", func() {
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
		r.NoError(err, "register tenant")
		s.DeferTenantCleanup(rt.ID)

		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
		r.NoError(err, "provision user")

		s.pat = p.PAT
		s.management = gateway.NewClientForTenant(s.Cfg, p.PAT, rt.ID)
		s.picking = gateway.NewPickingClientForTenant(s.Cfg, p.PAT, rt.ID)
	}) {
		return
	}

	if !s.Run("datatype without a binding", func() {
		s.slug = fmt.Sprintf("e2e-dataindex-%d", time.Now().UnixNano())
		name := "E2E Data Index Order Type"

		created, err := s.management.CreateDataType(s.Ctx, managementapi.CreateDataTypeArgs{
			Input: managementapi.CreateDataTypeInput{
				Name:       &name,
				Slug:       &s.slug,
				Entity:     "picking_order",
				JSONSchema: schemaWithoutBinding,
			},
		})
		r.NoError(err, "create datatype")
		s.dataTypeID, err = uuid.Parse(created.GetCreateDataType().ID)
		r.NoError(err, "datatype id must be a uuid")
	}) {
		return
	}

	if !s.Run("order written before the binding exists", func() {
		// This row is the one the backfill has to repair: the projection hook
		// cannot have filled a slot that was not bound when it ran.
		created, err := s.picking.CreatePickingOrder(s.Ctx, pickingapi.CreatePickingOrderArgs{
			Input: pickingmodel.CreatePickingOrderWithItemsInput{
				DataTypeID: &s.dataTypeID,
				Data:       map[string]any{"serials": []any{indexedSerial}},
			},
		})
		r.NoError(err, "create order")
		s.orderID = created.GetCreatePickingOrder().PickingOrder.ID
		r.NotEmpty(s.orderID)
	}) {
		return
	}

	if !s.Run("binding applied to the live stack as a new version", func() {
		name := "E2E Data Index Order Type"
		created, err := s.management.CreateDataType(s.Ctx, managementapi.CreateDataTypeArgs{
			Input: managementapi.CreateDataTypeInput{
				Name:       &name,
				Slug:       &s.slug,
				Entity:     "picking_order",
				JSONSchema: schemaWithBinding,
			},
		})
		r.NoError(err, "add x-indices as a new version")
		r.Equal(2, created.GetCreateDataType().Version, "same slug must append version 2")
	}) {
		return
	}

	if !s.Run("the pre-existing row becomes findable without a restart", func() {
		// Polls the whole chain: the NATS invalidation reaching picking's cache,
		// then the backfill projecting a row written before the binding.
		r.NoError(tests.PollUntil(s.Ctx, indexVisibleTimeout, pollInterval, func() error {
			ids, err := s.ordersByOverlaps([]string{indexedSerial})
			if err != nil {
				return err
			}
			if len(ids) != 1 || ids[0] != s.orderID {
				return fmt.Errorf("want the seeded order, got %v", ids)
			}
			return nil
		}), "order must become findable through the data index")
	}) {
		return
	}

	s.Run("a serial no order carries matches nothing", func() {
		ids, err := s.ordersByOverlaps([]string{unusedSerial})
		r.NoError(err)
		r.Empty(ids, "an unclaimed serial must not match")
	})

	if !s.Run("deleting the bound version re-points picking's cache", func() {
		// The delete event's payload carries the row; picking's cache uses its
		// slug to re-point the slug slot at the surviving version. v1 has no
		// binding, so the "serialNumbers" index must STOP resolving — if the
		// slot keeps serving the deleted v2 (a live-looking cached copy), this
		// poll never converges. This is the end-to-end seam the Bruno deletion
		// suite misses: dataTypeBySlug is a management DB query and never
		// touches the downstream cache.
		v2, err := s.latestVersionID()
		r.NoError(err, "resolve v2 id")
		_, err = s.management.DeleteDataType(s.Ctx, managementapi.DeleteDataTypeArgs{Id: v2})
		r.NoError(err, "delete the bound version")

		r.NoError(tests.PollUntil(s.Ctx, indexVisibleTimeout, pollInterval, func() error {
			_, err := s.ordersByOverlaps([]string{indexedSerial})
			if err == nil {
				return fmt.Errorf("index still resolves; picking's slug slot still serves the deleted version")
			}
			// dataindex.ErrUnknownIndex: v1 declares no bindings, so the
			// index name no longer resolves — proof the slot re-pointed.
			if !strings.Contains(err.Error(), "no index with that name") {
				return fmt.Errorf("expected the promoted binding-less v1 (no index with that name), got: %w", err)
			}
			return nil
		}), "picking must promote the surviving version after the delete event")
	}) {
		return
	}

	if !s.Run("deleting the last version drops the slug slot", func() {
		v1, err := s.latestVersionID()
		r.NoError(err, "resolve v1 id")
		_, err = s.management.DeleteDataType(s.Ctx, managementapi.DeleteDataTypeArgs{Id: v1})
		r.NoError(err, "delete the last version")

		r.NoError(tests.PollUntil(s.Ctx, indexVisibleTimeout, pollInterval, func() error {
			_, err := s.ordersByOverlaps([]string{indexedSerial})
			if err == nil {
				return fmt.Errorf("index still resolves; slug slot still holds a deleted version")
			}
			if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "deleted") {
				return fmt.Errorf("expected not-found/deleted for a slug with no live version, got: %w", err)
			}
			return nil
		}), "the slug slot must not serve a family with no live versions")
	}) {
		return
	}

	s.Run("a multi-tenant query is refused, not a 500", func() {
		// The datatype resolves per tenant, so this read cannot serve several at
		// once. It must say so rather than take the process down. Needs the
		// system token: a tenant-scoped PAT resolves "all" to its single tenant,
		// which is a perfectly serviceable query.
		client := gateway.NewPickingClientForTenant(s.Cfg, s.Cfg.ServiceToken, "all")
		_, err := client.GetPickingOrders(s.Ctx, pickingapi.GetPickingOrdersArgs{
			First: ptr(10),
			Where: s.whereSerial([]string{indexedSerial}),
		})
		r.Error(err, "dataIndex across several tenants must be refused")
		r.Contains(err.Error(), "exactly one tenant",
			"must be refused by the dataIndex guard, not by something else")
	})
}
