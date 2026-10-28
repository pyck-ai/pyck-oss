// Package admission bounds how many executions of a named GraphQL operation
// one replica runs at a time, and refuses the rest immediately.
//
// It exists for mutations that are expensive or reachable without
// authentication: without a bound, the only thing standing between such an
// operation and the writer connection pool is how fast a caller can open
// HTTP requests, and a pool held by N slow mutations is a pool every other
// request is queued behind. A refusal that costs nothing is a better answer
// than a request that waits for a connection it will not get in time.
//
// Register the limiter BEFORE the transaction middleware on the gqlgen
// server: gqlgen runs operation interceptors in registration order, so a
// refusal here happens before any connection is checked out.
package admission

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/pyck-ai/pyck/backend/common/gqltx"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// ErrTooManyInFlight is the refusal a caller gets when the operation it
// asked for already has its configured number of executions running on this
// replica. It is a statement about right now: the same request sent later
// is admitted.
var ErrTooManyInFlight = errors.New("too many concurrent executions of this operation")

// admissionOutcomes counts every decision the limiter makes, labelled by
// the top-level field and "admitted" or "refused". A cap that is being hit
// is otherwise visible only in a response body the client keeps: the
// refused operation never reaches the transaction middleware, so none of
// the gqltx counters move either.
var admissionOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "admission_operation_outcomes_total",
	Help: "Admission decisions per limited GraphQL operation",
}, []string{"field", "outcome"})

// Limiter is a gqlgen operation interceptor that caps the in-flight
// executions of the operations it was configured with. Operations it does
// not name are passed through untouched. Safe for concurrent use; construct
// one per server.
type Limiter struct {
	// slots holds one buffered channel per limited top-level field; its
	// capacity is that field's cap and its length the number of executions
	// in flight. The map itself is immutable after New, which is what makes
	// the Limiter safe to share.
	slots map[string]chan struct{}
	// refusalLog keeps a saturated cap from writing one line per refused
	// request: the load that saturates it is exactly the load that would
	// then flood the log pipeline. The suppressed count on each emitted
	// line is how many refusals the window hid; the counter has the exact
	// number. One throttle per Limiter, shared by its fields: a service
	// limits a handful of operations, and an operator reading "refused"
	// wants the loudest one, not one line per field per window.
	refusalLog *log.FaultThrottle
}

// New returns a Limiter that admits at most limits[field] concurrent
// executions of each named top-level GraphQL field. A cap of zero or less
// leaves that field unbounded, which is the documented way to switch the
// bound off through configuration.
func New(limits map[string]int) *Limiter {
	slots := make(map[string]chan struct{}, len(limits))

	for field, limit := range limits {
		if limit <= 0 {
			continue
		}
		slots[field] = make(chan struct{}, limit)
	}

	return &Limiter{slots: slots, refusalLog: log.NewFaultThrottle(log.FaultLogWindow)}
}

// ExtensionName implements graphql.HandlerExtension.
func (l *Limiter) ExtensionName() string { return "Admission" }

// Validate implements graphql.HandlerExtension.
func (l *Limiter) Validate(graphql.ExecutableSchema) error { return nil }

// InterceptOperation admits the operation or refuses it, and releases the
// slots once the response handler has produced its response.
//
// One slot is taken per distinct limited field the operation selects:
// fields reached through inline fragments and fragment spreads included,
// because the document is written by the caller and a fragment wrapper
// would otherwise hide the field from its own cap, and one slot however
// often the document repeats the field, because aliases would otherwise
// let one request take the whole cap. An operation that cannot take
// every slot it needs takes none: it is refused, and what it had already
// taken is handed back before the refusal is written.
//
// The refusal is a GraphQL error at the operation's top-level field so the
// federated gateway surfaces it to the client; it carries no retry hint
// because the caller cannot tell from it whether the replica or the fleet
// is busy.
func (l *Limiter) InterceptOperation(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	taken, full := l.acquire(gqltx.TopLevelFieldNames(ctx))
	if full != "" {
		release(taken)
		l.refusalLog.Warn(log.ForContext(ctx)).
			Err(ErrTooManyInFlight).
			Str("field", full).
			Int("cap", cap(l.slots[full])).
			Msg("operation refused by the admission cap")

		return graphql.OneShot(gqltx.ErrorResponseAtTopField(ctx, fmt.Errorf("%s: %w", full, ErrTooManyInFlight)))
	}

	if len(taken) == 0 {
		return next(ctx)
	}

	// A panic before the response handler exists, in a later interceptor
	// or in the executor, is recovered by gqlgen's server, and no handler
	// is ever called to release the slots: hand them back here instead.
	built := false
	defer func() {
		if !built {
			release(taken)
		}
	}()
	resp := next(ctx)
	built = true

	// The slots are released on the first call of the response handler,
	// which is where the transaction middleware also finalizes. The defer
	// covers a panic in a resolver that gqlgen's recover middleware turns
	// into an error response.
	//
	// Once, because a handler is called until it answers nil: that is how
	// gqlgen's websocket and incremental-delivery loops terminate, and a
	// second release would drain a channel this operation no longer has a
	// token in, blocking the caller's goroutine until another execution
	// refills it, and then taking that execution's slot. The services ship
	// neither transport today, which makes this the cheap way to keep the
	// property from depending on that staying true.
	var once sync.Once

	return func(callCtx context.Context) *graphql.Response {
		defer once.Do(func() { release(taken) })
		return resp(callCtx)
	}
}

// acquire takes one slot for every distinct limited field in fields and
// returns them. On the first field whose cap is exhausted it stops and
// returns that field's name; the slots taken up to there are returned too,
// for the caller to hand back.
//
// Distinct is the load-bearing word: fields is the caller's document, and
// `mutation { a: f(...) b: f(...) … }` names one field under N aliases. One
// slot per selection would let a single request hold the whole cap of f and
// deny it to every other caller of this replica for as long as that request
// runs, which is the opposite of what a cap is for. A repeat costs the
// operation nothing extra here; what the repeated executions cost is
// bounded elsewhere: a document repeating a transaction-exempt field is
// refused outright (gqltx.ErrMixedTransactionExemption), and any other
// repeated field runs inside the single writer transaction its operation
// already holds.
func (l *Limiter) acquire(fields []string) (taken []chan struct{}, full string) {
	// Held names the fields this operation already holds a slot of. A
	// slice rather than a map: an operation limits a handful of fields, so
	// the scan is cheaper than the allocation, and an operation that names
	// no limited field allocates nothing.
	var held []string

	for _, field := range fields {
		slot, limited := l.slots[field]
		if !limited || slices.Contains(held, field) {
			continue
		}

		select {
		case slot <- struct{}{}:
			admissionOutcomes.WithLabelValues(field, "admitted").Inc()
			held = append(held, field)
			taken = append(taken, slot)
		default:
			admissionOutcomes.WithLabelValues(field, "refused").Inc()
			return taken, field
		}
	}

	return taken, ""
}

// release hands back every slot in taken. Draining is never blocking: each
// channel in taken holds the token this operation put there.
func release(taken []chan struct{}) {
	for _, slot := range taken {
		<-slot
	}
}
