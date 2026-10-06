//go:build integration

package jsondatafilter_test

import (
	"fmt"

	"github.com/google/uuid"
)

// serviceTarget is one entity with a data column per service: the connection
// field the filters are sent to, the DataType entity label, and the mutation
// that stores a row with the given data. The data filter resolvers are the
// same code for every entity of a service, so one entity per service covers
// the service.
type serviceTarget struct {
	service      string
	field        string
	entity       string
	seedMutation func(dataTypeID string) string
}

func serviceTargets() []serviceTarget {
	name := func() string { return gqlString("jdf-" + uuid.NewString()) }
	return []serviceTarget{
		{"file", "files", "file", func(dt string) string {
			return fmt.Sprintf(`mutation($data: Map!) { createFile(input: {data: $data, dataTypeID: %s, refid: %s, reftype: item, name: %s, contentType: "application/json", size: 1}) { id } }`,
				gqlString(dt), gqlString(uuid.NewString()), name())
		}},
		{"main-data", "customers", "customer", func(dt string) string {
			return fmt.Sprintf(`mutation($data: Map!) { createCustomer(input: {data: $data, dataTypeID: %s}) { id } }`, gqlString(dt))
		}},
		{"management", "devices", "device", func(dt string) string {
			return fmt.Sprintf(`mutation($data: Map!) { createDevice(input: {data: $data, dataTypeID: %s, name: %s}) { device { id } } }`, gqlString(dt), name())
		}},
		{"picking", "pickingOrders", "picking_order", func(dt string) string {
			return fmt.Sprintf(`mutation($data: Map!) { createPickingOrder(input: {data: $data, dataTypeID: %s}) { pickingOrder { id } } }`, gqlString(dt))
		}},
		{"receiving", "receivingInbounds", "inbound", func(dt string) string {
			return fmt.Sprintf(`mutation($data: Map!) { createReceivingInbound(input: {data: $data, dataTypeID: %s}) { receivingInbound { id } } }`, gqlString(dt))
		}},
		{"workflow", "workflows", "workflow", func(dt string) string {
			n := name()
			return fmt.Sprintf(`mutation($data: Map!) { registerWorkflow(input: {data: $data, dataTypeID: %s, name: %s, taskQueue: %s, workerID: %s}) { id } }`, gqlString(dt), n, n, n)
		}},
		{"inventory", "inventoryItems", "item", func(dt string) string {
			return fmt.Sprintf(`mutation($data: Map!) { createInventoryItem(input: {data: $data, dataTypeID: %s, sku: %s}) { inventoryItem { id } } }`, gqlString(dt), name())
		}},
	}
}
