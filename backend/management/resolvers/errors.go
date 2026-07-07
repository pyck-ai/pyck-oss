package resolvers

import "errors"

// ErrOrganizationForbidden is returned when a non-system caller invokes
// the organization query. The query is the auth-path probe every
// backend service makes after introspection; opening it to non-system
// callers would let any authenticated user fish for arbitrary users'
// org state.
var ErrOrganizationForbidden = errors.New("organization: forbidden — system caller required")
