package workflow_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/log"
	commonwf "github.com/pyck-ai/pyck/backend/common/workflow"
)

// errType is the step name a classified failure is reported under; the
// value itself is opaque to the classifier but is read by the resolver
// message and the end-to-end suites, so it has to survive unchanged.
const errType = "test-step-rejected"

// httpStatusTestError is a dependency error that carries an HTTP status, the shape
// backend/management/workerapi.StatusError has.
type httpStatusTestError struct {
	code int
}

func (e *httpStatusTestError) Error() string   { return fmt.Sprintf("worker-api said %d", e.code) }
func (e *httpStatusTestError) HTTPStatus() int { return e.code }

// assertRetryable checks Classify handed the error back untouched: a
// retryable outcome must not become an application error, because that is
// what the activity's retry policy keys on.
func assertRetryable(t *testing.T, in, got error) {
	t.Helper()
	require.Equal(t, in, got, "a retryable error must come back unchanged, not wrapped")
	var appErr *temporal.ApplicationError
	if errors.As(got, &appErr) {
		assert.False(t, appErr.NonRetryable(), "%v must stay retryable", got)
	}
}

// assertPermanent checks Classify turned the error into a non-retryable
// application error typed with the failing step and still carrying the
// cause, which is what ends the activity on its first attempt.
//
// The assertion reads the converted failure rather than errors.As,
// because that is what the SDK sends: it type-switches on the error the
// activity returned, so a verdict reachable only through a wrapper
// travels as a retryable failure of type "wrapError" while errors.As
// still finds it.
func assertPermanent(t *testing.T, cause, got error) {
	t.Helper()
	info := temporal.GetDefaultFailureConverter().ErrorToFailure(got).GetApplicationFailureInfo()
	require.NotNil(t, info, "a permanent outcome must travel as an application failure, got %v", got)
	assert.True(t, info.GetNonRetryable(), "%v must be non-retryable on the wire", got)
	assert.Equal(t, errType, info.GetType(), "the error type names the failing step")
	require.ErrorIs(t, got, cause, "the cause must stay reachable for the run's error message")
}

// wantPermanentCodes is the oracle for the retry decision, written out
// here instead of read from the table under test: every code in this list
// is a refusal (the same request gets the same answer) and every other
// code is a fault the activity's retry budget may outlive.
//
// Deliberately absent, because each is transient and belongs to the
// retryable side: Unauthenticated (a credential rejected by a rotation
// that just happened or one not yet picked up; calling it permanent
// fails a whole registration on a token that heals by itself),
// Unavailable, DeadlineExceeded, ResourceExhausted, Aborted, Canceled,
// Internal, DataLoss, Unknown and OK.
var wantPermanentCodes = []codes.Code{
	codes.InvalidArgument,
	codes.NotFound,
	codes.AlreadyExists,
	codes.PermissionDenied,
	codes.FailedPrecondition,
	codes.OutOfRange,
	codes.Unimplemented,
}

// codesWhereTrue is the set of codes a verdict table calls permanent.
func codesWhereTrue(table map[codes.Code]bool) []codes.Code {
	permanent := make([]codes.Code, 0, len(table))
	for code, verdict := range table {
		if verdict {
			permanent = append(permanent, code)
		}
	}
	return permanent
}

// The verdict per code is a product decision, so it needs an oracle
// outside the table: a test that reads its expectation out of
// permanentCodes moves with any row someone flips, and would let
// Unauthenticated become permanent (a transient credential rejection
// failing a registration outright) without going red.
func TestPermanentCodes_MatchTheDocumentedVerdicts(t *testing.T) {
	t.Parallel()

	require.ElementsMatch(t, wantPermanentCodes, codesWhereTrue(commonwf.PermanentCodes),
		"the table's permanent codes must be exactly the refusals; every other code is a fault a retry may outlive")
}

// Every gRPC code the platform's dependencies answer with has to land on
// one side of the retry decision deliberately: a code wrongly called
// permanent fails a registration a restart would have survived, and one
// wrongly called retryable spends the whole activity budget on an answer
// that will not change.
//
// The loop walks the whole code space rather than a hand-kept list, so a
// code the gRPC package adds (or one someone drops from the table)
// fails here instead of silently taking the retryable default. The
// expectation comes from wantPermanentCodes, not from the table, so
// Classify is checked against the intended verdict rather than against
// whatever the table currently says.
func TestClassify_EveryGRPCCode(t *testing.T) {
	t.Parallel()

	for code := codes.OK; code <= codes.Unauthenticated; code++ {
		_, classified := commonwf.PermanentCodes[code]
		require.True(t, classified,
			"%s carries no verdict; the default branch would call it retryable", code)
		wantPermanent := slices.Contains(wantPermanentCodes, code)

		if code == codes.OK {
			// Not an error: Classify is never called with one, and
			// status.Error(codes.OK, …) is a nil error.
			continue
		}

		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()
			in := status.Error(code, "dependency said "+code.String())

			got := commonwf.Classify(t.Context(), errType, in)

			if wantPermanent {
				assertPermanent(t, in, got)
				return
			}
			assertRetryable(t, in, got)
		})
	}
}

// A code outside the table (one a future grpc-go adds, or one a
// dependency invents) must take the unlisted path deliberately: retryable
// AND logged. Without the line the verdict is indistinguishable from a
// listed false, so an operator reading a run that spent its whole budget
// has nothing naming the code nobody classified.
func TestClassify_AnUnlistedCodeIsRetryableAndLogged(t *testing.T) {
	t.Parallel()

	unlisted := codes.Code(math.MaxUint32)
	_, listed := commonwf.PermanentCodes[unlisted]
	require.False(t, listed, "the probe code must be one the table does not carry")

	var buf bytes.Buffer
	ctx := log.Context(t.Context(), zerolog.New(&buf))
	in := status.Error(unlisted, "a dependency answered with a code nobody classified")

	got := commonwf.Classify(ctx, errType, in)

	assertRetryable(t, in, got)
	assert.Contains(t, buf.String(), "unclassified error code",
		"an unlisted code must be visible to an operator, not silently retryable")
}

// A gRPC status reaches the classifier through the layers of %w-wrapping
// the activities add to say which call failed; classification must read
// through them rather than seeing an opaque error.
func TestClassify_ReadsThroughWrapping(t *testing.T) {
	t.Parallel()

	cause := status.Error(codes.InvalidArgument, "org name is not valid")
	wrapped := fmt.Errorf("create organization %q: %w", "x", fmt.Errorf("zitadel: %w", cause))

	got := commonwf.Classify(t.Context(), errType, wrapped)

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, got, &appErr)
	assert.True(t, appErr.NonRetryable())
	require.ErrorIs(t, got, cause)
	assert.Contains(t, got.Error(), "create organization", "the wrapping context must stay in the message")
}

// Temporal's serviceerror types carry Status(), not GRPCStatus(), so a
// classifier built on status.Code(err) reads every one of them as Unknown
// and retries a namespace Temporal will never accept.
func TestClassify_TemporalServiceErrors(t *testing.T) {
	t.Parallel()

	t.Run("a namespace that already exists is permanent", func(t *testing.T) {
		t.Parallel()
		in := serviceerror.NewNamespaceAlreadyExists("ns")

		assert.Equal(t, codes.Unknown, status.Code(in),
			"guard: plain status.Code cannot see a serviceerror, which is why Classify must not use it")

		assertPermanent(t, in, commonwf.Classify(t.Context(), errType, in))
	})

	t.Run("an invalid namespace argument is permanent", func(t *testing.T) {
		t.Parallel()
		in := serviceerror.NewInvalidArgument("namespace name is invalid")
		assertPermanent(t, in, commonwf.Classify(t.Context(), errType, in))
	})

	t.Run("an unavailable server is retryable", func(t *testing.T) {
		t.Parallel()
		in := serviceerror.NewUnavailable("frontend is restarting")
		assertRetryable(t, in, commonwf.Classify(t.Context(), errType, in))
	})
}

// A context that ran out is a statement about this attempt, not about the
// request: the next attempt gets a fresh one.
func TestClassify_ContextErrorsAreRetryable(t *testing.T) {
	t.Parallel()

	for _, in := range []error{context.DeadlineExceeded, context.Canceled} {
		t.Run(in.Error(), func(t *testing.T) {
			t.Parallel()
			assertRetryable(t, in, commonwf.Classify(t.Context(), errType, in))
		})
	}
}

// worker-api answers over HTTP, so its outcomes are classified by status
// class: a request it refuses is permanent, a fault of its own is not,
// and the two credential answers split the way their gRPC counterparts
// do: 401 retryable like codes.Unauthenticated, 403 permanent like
// codes.PermissionDenied.
func TestClassify_HTTPStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code      int
		permanent bool
	}{
		{http.StatusBadRequest, true},
		// The identity is accepted and lacks the grant, the mirror of
		// codes.PermissionDenied; 401 next to it is the credential being
		// rejected, which a rotation heals.
		{http.StatusForbidden, true},
		{http.StatusNotFound, true},
		{http.StatusConflict, true},
		{http.StatusUnprocessableEntity, true},

		{http.StatusUnauthorized, false},
		{http.StatusRequestTimeout, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusGatewayTimeout, false},
	}

	for _, tc := range cases {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			t.Parallel()
			in := &httpStatusTestError{code: tc.code}

			got := commonwf.Classify(t.Context(), errType, in)

			if tc.permanent {
				assertPermanent(t, in, got)
				return
			}
			assertRetryable(t, in, got)
		})
	}
}

// The HTTP status has to win over the transport status: a wrapped 4xx
// carries no gRPC code, so a classifier that looked at the code first
// would call every worker-api refusal Unknown and retry it.
func TestClassify_HTTPStatusIsReadThroughWrapping(t *testing.T) {
	t.Parallel()

	cause := &httpStatusTestError{code: http.StatusUnprocessableEntity}
	wrapped := fmt.Errorf("create deployment: %w", cause)

	assertPermanent(t, cause, commonwf.Classify(t.Context(), errType, wrapped))
}

// An activity that already decided the outcome names its own step; a
// second classification would bury that name under the generic one.
func TestClassify_KeepsAnExistingNonRetryableVerdict(t *testing.T) {
	t.Parallel()

	cause := status.Error(codes.AlreadyExists, "username taken")
	in := temporal.NewNonRetryableApplicationError("admin username is taken", "admin-username-taken", cause)

	got := commonwf.Classify(t.Context(), errType, in)

	info := temporal.GetDefaultFailureConverter().ErrorToFailure(got).GetApplicationFailureInfo()
	require.NotNil(t, info)
	assert.True(t, info.GetNonRetryable())
	assert.Equal(t, "admin-username-taken", info.GetType(), "the specific step must survive the outer classification")
	assert.Contains(t, got.Error(), "admin username is taken", "the step's own message must survive")
	require.ErrorIs(t, got, cause, "the dependency's error must stay reachable")
}

// A verdict an activity wrapped on its way out must still reach the
// server as one: the SDK converts an activity error by a concrete type
// switch, so a *temporal.ApplicationError inside a fmt.Errorf becomes a
// retryable "wrapError" failure and the step retries for its whole
// budget under a step name nobody can read.
func TestClassify_RestatesAWrappedNonRetryableVerdict(t *testing.T) {
	t.Parallel()

	cause := status.Error(codes.AlreadyExists, "username taken")
	verdict := temporal.NewNonRetryableApplicationError("admin username is taken", "admin-username-taken", cause)

	got := commonwf.Classify(t.Context(), errType, fmt.Errorf("import admin user: %w", verdict))

	info := temporal.GetDefaultFailureConverter().ErrorToFailure(got).GetApplicationFailureInfo()
	require.NotNil(t, info, "the wrapped verdict must be restated as an application failure, got %v", got)
	assert.True(t, info.GetNonRetryable(), "a wrapped verdict must not be retried")
	assert.Equal(t, "admin-username-taken", info.GetType(), "the inner step's type must survive the wrapper")
	require.ErrorIs(t, got, cause, "the cause must stay reachable for the run's error message")
	assert.Contains(t, got.Error(), "import admin user", "the wrapper's message must survive")
}

// An error nothing enumerated must not fail a registration a retry would
// have completed: the activity's schedule-to-close budget is the bound.
func TestClassify_UnrecognisedErrorsAreRetryable(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]error{
		"plain error":       errors.New("connection reset by peer"),
		"database error":    fmt.Errorf("query tenant: %w", errors.New("driver: bad connection")),
		"retryable app err": temporal.NewApplicationError("transient", "some-step"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertRetryable(t, in, commonwf.Classify(t.Context(), errType, in))
		})
	}
}

// A successful activity must not be given an error to fail on.
func TestClassify_NilIsNil(t *testing.T) {
	t.Parallel()

	require.NoError(t, commonwf.Classify(t.Context(), errType, nil))
}

// An activity that explicitly left the outcome to the retry policy keeps
// that verdict even when the cause underneath carries a code the table
// calls permanent: the register-tenant user imports return exactly this
// shape for an AlreadyExists whose holder the projection cannot attribute
// yet.
func TestClassify_KeepsAnExistingRetryableVerdict(t *testing.T) {
	t.Parallel()

	cause := status.Error(codes.AlreadyExists, "User already exists (V3-DKcYh)")
	in := temporal.NewApplicationErrorWithOptions("username holder not projected yet", "admin-username-taken",
		temporal.ApplicationErrorOptions{Cause: cause})

	got := commonwf.Classify(t.Context(), errType, in)

	assertRetryable(t, in, got)
	assert.Equal(t, codes.AlreadyExists, status.Code(got),
		"the dependency's own answer must stay reachable in the chain")
}
