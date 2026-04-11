package model

import "github.com/google/uuid"

type CustomEvent struct {
	TenantID *uuid.UUID
	UserID   *uuid.UUID
	Type     string
	Data     map[string]interface{}
}
