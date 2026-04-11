package registry

import (
	"errors"
)

var (
	ErrInvalidWorkflowType       = errors.New("invalid workflow type")
	ErrInvalidActivityType       = errors.New("invalid activity type")
	ErrWorkflowAlreadyRegistered = errors.New("workflow already registered")
)
