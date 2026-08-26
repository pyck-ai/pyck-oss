//go:build integration

package dataindex

import (
	"fmt"

	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	pickingmodel "github.com/pyck-ai/pyck/backend/picking/model"
)

// whereSerial builds the dataIndex filter the serial-claim lookup uses: an
// overlap against the list slot, scoped to the suite's datatype by slug.
func (s *DataIndexSuite) whereSerial(serials []string) *pickingapi.PickingOrderWhereInput {
	return &pickingapi.PickingOrderWhereInput{
		DataIndex: &pickingmodel.DataIndexWhereInput{
			DataType: s.slug,
			Field:    "serialNumbers",
			Overlaps: serials,
		},
	}
}

// ordersByOverlaps returns the ids of the orders whose indexed serials overlap
// the candidates.
func (s *DataIndexSuite) ordersByOverlaps(serials []string) ([]string, error) {
	resp, err := s.picking.GetPickingOrders(s.Ctx, pickingapi.GetPickingOrdersArgs{
		First: ptr(50),
		Where: s.whereSerial(serials),
	})
	if err != nil {
		return nil, fmt.Errorf("query orders by data index: %w", err)
	}

	edges := resp.GetPickingOrders().Edges
	ids := make([]string, 0, len(edges))
	for _, e := range edges {
		if e == nil || e.Node == nil {
			continue
		}
		ids = append(ids, e.Node.ID)
	}
	return ids, nil
}

func ptr[T any](v T) *T { return &v }
