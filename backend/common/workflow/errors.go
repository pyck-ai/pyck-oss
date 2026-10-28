package workflow

import (
	"context"
	"errors"
	"net/http"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// httpStatusError is implemented by an error that carries the HTTP status
// of the response it reports, so a REST dependency is classified by the
// same rule as a gRPC one. backend/management/workerapi.StatusError is one.
type httpStatusError interface {
	HTTPStatus() int
}

// Classify is the last thing an activity does to its error: it decides
// whether the activity's retry policy may retry it.
//
// A refusal (an answer no retry can change, because the dependency
// rejects the request on its own terms) comes back as a non-retryable
// application error of type errType, so the activity ends on its first
// attempt and the workflow error names the failing step. A fault
// (everything else, transport failures included) comes back unchanged and
// is retried under the activity's retry policy. That policy must be
// bounded (attempts or ScheduleToCloseTimeout), or a dependency that never
// recovers is retried forever. errType is read by callers and by the
// end-to-end suites, so it names the step, not the status code.
//
// The decision reads only the status code, so anything the code cannot
// carry stays with the activity and has to be settled before this runs:
// an "already exists" that a lookup resolves as this run's own earlier
// write, or a NotFound on an object this run created whose read model has
// merely not caught up. Classifying first would report a retry-safe
// outcome as a permanent failure.
//
// A verdict the activity already made (an application error in the
// chain) is kept with its own error type. A non-retryable one is restated
// at the top level: the SDK's failure converter type-switches on the
// concrete error the activity returns, so a verdict reachable only through
// a fmt.Errorf wrapper would cross the wire as a retryable failure of type
// "wrapError". The whole chain stays the cause, so the wrapper's message
// and the dependency's error survive. A retryable verdict comes back
// unchanged: the activity had strictly more knowledge than the table.
func Classify(ctx context.Context, errType string, err error) error {
	if err == nil {
		return nil
	}

	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		if appErr.NonRetryable() {
			return temporal.NewApplicationErrorWithOptions(appErr.Message(), appErr.Type(),
				temporal.ApplicationErrorOptions{NonRetryable: true, Cause: err})
		}
		return err
	}

	if !permanent(ctx, err) {
		return err
	}
	return temporal.NewNonRetryableApplicationError(err.Error(), errType, err)
}

// permanentCodes is the verdict per gRPC status code: true means the code
// is a refusal (repeating the same request gets the same answer) and
// false means it is a fault a retry inside the activity's budget may
// outlive. Every code the platform's dependencies return is listed here;
// see permanent for what an unlisted one does.
var permanentCodes = map[codes.Code]bool{
	// Not an error at all. permanent is only called with a non-nil error,
	// so this entry exists to keep the table total rather than to classify
	// anything.
	codes.OK: false,

	// Our side of the wire: the request is malformed or a value in it is
	// rejected. The same request is rejected again.
	codes.InvalidArgument: true,

	// Their side: the object the request names is not there. For an object
	// this run wrote a moment ago the answer may be a read model that lags
	// the write; that is the activity's knowledge, settled before Classify
	// runs.
	codes.NotFound: true,

	// Their side, settled: the name or key belongs to something else. A
	// holder that is this run's own earlier write is resolved by a lookup
	// before Classify runs.
	codes.AlreadyExists: true,

	// Our side, settled: the caller's identity is known and lacks the grant
	// this call needs. Repeating the call does not grant it.
	codes.PermissionDenied: true,

	// Their side: the object is in a state that forbids the call, and
	// nothing in this run moves it out of that state. Zitadel also answers
	// this for an object whose projection lags, the same own-write case as
	// NotFound.
	codes.FailedPrecondition: true,

	// Our side: the argument lies outside the range the API accepts.
	codes.OutOfRange: true,

	// Their side: the deployed dependency does not serve this call.
	codes.Unimplemented: true,

	// Their side, transient: the credential was rejected, which a rotation
	// that has just happened or one still to be picked up both produce.
	// Retrying inside the activity's budget is cheaper than failing the
	// work on a credential that heals by itself; a caller holding a
	// long-lived client spends that budget against the same token and then
	// fails.
	codes.Unauthenticated: false,

	// Their side, transient: a restart, a rollout, a closed connection.
	codes.Unavailable: false,

	// Transport: the call ran out of time, not out of answers.
	codes.DeadlineExceeded: false,

	// Their side, transient: a quota or rate limit that refills.
	codes.ResourceExhausted: false,

	// Their side, transient: a concurrency conflict a retry resolves.
	codes.Aborted: false,

	// Our side: the activity's context ended. That is a statement about
	// this attempt, not about the request.
	codes.Canceled: false,

	// Their side: a bug or a broken invariant, which a fix deployed while
	// the run still has budget clears.
	codes.Internal: false,

	// Their side: corrupt or lost data, which recovery may restore.
	codes.DataLoss: false,

	// Everything that carries no status: transport failures, database
	// errors, plain errors. Retried within the activity's budget.
	codes.Unknown: false,
}

// permanent reports whether err is a refusal rather than a fault: whether
// repeating the same request gets the same answer. A code nobody
// enumerated counts as a fault, because the activity's retry budget bounds
// a wrong "retryable" while a wrong "permanent" fails work that would have
// succeeded.
func permanent(ctx context.Context, err error) bool {
	var httpErr httpStatusError
	if errors.As(err, &httpErr) {
		return permanentHTTPStatus(httpErr.HTTPStatus())
	}

	// serviceerror.ToStatus, not status.Code: Temporal's serviceerror types
	// carry Status() rather than GRPCStatus(), so status.Code reports them
	// all as Unknown. It also maps a wrapped gRPC status and the two
	// context errors, and answers Unknown for a plain error.
	code := serviceerror.ToStatus(err).Code()
	verdict, known := permanentCodes[code]
	if !known {
		log.ForContext(ctx).Warn().Str("grpc_code", code.String()).Err(err).
			Msg("unclassified error code from a workflow activity, treating it as retryable")
		return false
	}
	return verdict
}

// permanentHTTPStatus classifies a REST response by its status class: 4xx
// is a verdict on the request, 5xx is a fault of the server, with three
// 4xx excepted.
func permanentHTTPStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		// The two 4xx that ask the caller to come back later.
		return false
	case http.StatusUnauthorized:
		// The HTTP side of the codes.Unauthenticated case above: the
		// credential was rejected, which a rotation just made or one not
		// yet picked up both produce, and which heals inside the
		// activity's budget. 403 stays permanent, like
		// codes.PermissionDenied: there the identity is accepted and
		// simply lacks the grant.
		return false
	}
	return code >= 400 && code < 500
}
