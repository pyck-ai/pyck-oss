package dto

import "github.com/google/uuid"

type ReceivingInboundItem struct {
	ReceivingInboundID uuid.UUID
	Sku                string
	Quantity           int64
}

type ReceivingInbound struct {
	OrderID               uuid.UUID
	SupplierID            uuid.UUID
	ReceivingInboundItems []ReceivingInboundItem
	Data                  map[string]interface{}
}
