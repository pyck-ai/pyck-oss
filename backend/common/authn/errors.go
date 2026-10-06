package authn

import "errors"

// ErrUnauthorized is the single rejection every authentication failure
// collapses into: the caller learns that the token was refused, never which
// of introspection, token state or the org-liveness gate refused it.
var ErrUnauthorized = errors.New("unauthorized")
