package authz

import (
	"errors"
)

var (
	ErrNoAuthInfo = errors.New("no auth info present")
	ErrAuthFailed = errors.New("authentication failed")
)
