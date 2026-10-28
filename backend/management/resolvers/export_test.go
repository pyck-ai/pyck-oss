package resolvers

import "time"

// SetRegistrationWait shortens the registerTenant wait and its poll
// interval so the external test package can exhaust the wait without
// spending the production budget.
func SetRegistrationWait(r *Resolver, wait, poll time.Duration) {
	r.registrationWait = wait
	r.registrationPoll = poll
}

// SetHashPassword replaces the admin password hash of registerTenant so
// the external test package can make it fail; bcrypt refuses no password
// the registration rules accept.
func SetHashPassword(r *Resolver, hash func(password []byte, cost int) ([]byte, error)) {
	r.hashPassword = hash
}

// The registerTenant refusals and failure answers, for errors.Is in the
// external test package.
var (
	ErrRegistrationFeaturesRefused = errRegistrationFeaturesRefused
	ErrTenantExists                = errTenantExists
	ErrRegistrationFailed          = errRegistrationFailed
	ErrRegistrationUnsettled       = errRegistrationUnsettled
	ErrRegistrationOutcomeUnknown  = errRegistrationOutcomeUnknown
	ErrRegistrationInternal        = errRegistrationInternal
)
