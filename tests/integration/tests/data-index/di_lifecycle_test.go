//go:build integration

package dataindex

import (
	"fmt"
	"time"

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
// order written under it, the binding applied live, and the query that must
// then find that order.
type DataIndexSuite struct {
	tests.Base

	pat        string
	slug       string
	dataTypeID string
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
		s.dataTypeID = created.GetCreateDataType().ID
		r.NotEmpty(s.dataTypeID)
	}) {
		return
	}

	if !s.Run("order written before the binding exists", func() {
		// This row is the one the backfill has to repair: the projection hook
		// cannot have filled a slot that was not bound when it ran.
		created, err := s.picking.CreatePickingOrder(s.Ctx, pickingapi.CreatePickingOrderArgs{
			Input: pickingmodel.CreatePickingOrderWithItemsInput{
				DataTypeSlug: &s.slug,
				Data:         map[string]any{"serials": []any{indexedSerial}},
			},
		})
		r.NoError(err, "create order")
		s.orderID = created.GetCreatePickingOrder().PickingOrder.ID
		r.NotEmpty(s.orderID)
	}) {
		return
	}

	if !s.Run("binding applied to the live stack", func() {
		schema := schemaWithBinding
		_, err := s.management.UpdateDataType(s.Ctx, managementapi.UpdateDataTypeArgs{
			Id:    s.dataTypeID,
			Input: managementapi.UpdateDataTypeInput{JSONSchema: &schema},
		})
		r.NoError(err, "add x-indices")
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
