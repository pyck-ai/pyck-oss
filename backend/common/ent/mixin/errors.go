package mixin

import (
	"errors"
)

var (
	ErrUnauthorized    = errors.New("unauthorized")
	ErrLimitExceeded   = errors.New("limit exceeded")
	ErrInvalidTenantID = errors.New("invalid tenant ID")
)
