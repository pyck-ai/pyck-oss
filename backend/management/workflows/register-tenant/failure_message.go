package registertenant

// failureAnswers maps the application error types whose producers emit
// them only for a refusal of the caller's own input to the fixed answer
// registerTenant gives for them: a type and a sentence. The operator-side
// types stay out: appErrTypeOrgMetadata (a missing grant, a Zitadel
// without the API, a value this binary cannot render) and
// appErrTypeAdminHashUnsupported (a Zitadel that does not accept pyck's
// bcrypt hash): telling an anonymous caller its input was refused would
// send it to rewrite valid data in vain. The application error's own
// message is never answered: it is the dependency's raw error (request
// paths, secret names, response bodies) and registerTenant is open to
// anonymous callers.
//
// A taken admin username is answered as a refused admin account, type
// included: usernames are unique across the whole Zitadel instance, so an
// answer of its own would let any caller probe whether a username exists
// anywhere on it, operators and service accounts included. The log keeps
// the real type.
var failureAnswers = map[string]string{
	appErrTypeOrganizationNameTaken: appErrTypeOrganizationNameTaken + ": an organization with this tenant name already exists",
	appErrTypeOrganizationInvalid:   appErrTypeOrganizationInvalid + ": the tenant name was refused",
	appErrTypeAdminUsernameTaken:    adminAccountRefused,
	appErrTypeAdminAccountInvalid:   adminAccountRefused,
	appErrTypeOrgMetadataInvalid:    appErrTypeOrgMetadataInvalid + ": the tenant data was refused",
}

const (
	// adminAccountRefused is the one answer for every refusal of the admin
	// account the caller can correct.
	adminAccountRefused = appErrTypeAdminAccountInvalid + ": the admin account was refused"

	// FailureGeneric is the answer for a failed registration step whose
	// error type has no caller message: a dependency fault, an
	// operator-side refusal, a timeout or a defect, none of which the
	// caller can correct in its request.
	FailureGeneric = "provisioning failed"
)

// FailureMessage returns the caller-safe answer for a failed registration
// step's application error type, or FailureGeneric when the type has no
// caller answer.
func FailureMessage(errType string) string {
	if answer, ok := failureAnswers[errType]; ok {
		return answer
	}
	return FailureGeneric
}
