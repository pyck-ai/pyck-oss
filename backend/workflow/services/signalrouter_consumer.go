package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
	"go.temporal.io/api/serviceerror"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
)

const (
	// stateChangeSubjectTokens is the token count of a Temporal state-change
	// subject: <stream>.<namespace>.temporal.<taskQueue>.<workflowTypeName>.<workflowID>.<runID>.<status>
	// (see TopicTypeTemporalWorkflowStateChangeEvent in the topic package).
	stateChangeSubjectTokens = 8

	// DefaultConsumerDurable is the durable name shared by every workflow
	// service replica. It is fixed: a second name would split the replicas
	// into two consumers and deliver every event twice.
	DefaultConsumerDurable = "workflow-signal-router"

	// DefaultConsumerAckWait is how long the server waits for an ack before
	// it redelivers. The router sends InProgress while a handler runs, so
	// this only has to cover a stalled or crashed replica.
	DefaultConsumerAckWait = 60 * time.Second

	// DefaultConsumerMaxAckPending bounds the events in flight across all
	// replicas.
	DefaultConsumerMaxAckPending = 256

	// DefaultConsumerMaxDeliver is the number of deliveries of one event
	// before the router gives up on it.
	DefaultConsumerMaxDeliver = 20

	// DefaultConsumerConcurrency is the number of events one replica handles
	// at the same time.
	DefaultConsumerConcurrency = 16

	// pullErrorPause is the wait after a failed pull before the next one.
	pullErrorPause = 500 * time.Millisecond

	// fetchWait is how long one pull request waits for events. It is also
	// the longest Stop waits for the pull loop to notice it.
	fetchWait = time.Second
)

// DefaultConsumerBackoff is the delay before redelivery after a transient
// failure, by delivery number. Deliveries past the last entry reuse it.
var DefaultConsumerBackoff = []time.Duration{ //nolint:gochecknoglobals
	time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute,
}

var eventsGivenUp = promauto.NewCounter( //nolint:gochecknoglobals
	prometheus.CounterOpts{
		Name: "workflow_signal_router_events_given_up_total",
		Help: "Total number of events the signal router gave up on after MaxDeliver failed deliveries",
	},
)

// ConsumerConfig tunes the router's durable JetStream consumer. Zero fields
// take the defaults above.
//
// The consumer is one durable pull consumer on the event stream, shared by all
// replicas, created or updated at Start. It filters to
//
//	<stream>.*.crud.*.*.*.*         mutation events
//	<stream>.*.temporal.*.*.*.*.*   Temporal workflow state changes
//
// so custom events, workflow events, attribute-change events, dead letters and
// request.reply.* subjects are never delivered. DeliverPolicy is New and only
// applies when the consumer is created: an existing consumer keeps its
// position, so a restart or a rolling deploy replays nothing and loses
// nothing.
//
// One window remains. The stream's ConsumerLimits delete a durable that has had
// no client for InactiveThreshold (72h), and the server rejects a higher value
// on the consumer (and treats 0 as "inherit the limit"), so the router cannot
// opt out. If no workflow replica pulls for longer than that, the durable is
// gone, Start recreates it at DeliverNew, and the events still in the stream
// that were published while it did not exist (up to the stream's MaxAge, 72h
// of them) are skipped. Raising the stream's ConsumerLimits above its MaxAge
// would close it; that is a change to the stream shared by every service.
//
// If Start finds the durable with an immutable field that differs from this
// config, it binds the durable as it is and logs a warning, rather than failing.
//
// Why an outage holds an event instead of NAKing it: a NAK, with or without
// delay, counts as a delivery (the redelivery carries NumDelivered+1), and
// the router gives up on an event after MaxDeliver deliveries (the consumer
// itself has no server-side limit, which would drop an event silently). NAKing
// through an outage would use up attempts on events with nothing wrong, and
// the give-up ack would then drop them. JetStream has no NAK that does not
// count, so the router keeps the event un-acked and sends InProgress instead,
// which resets AckWait without counting a delivery. The cost: a replica that
// dies while holding frees the event only after AckWait, and a held event
// occupies one of the MaxAckPending slots.
type ConsumerConfig struct {
	Durable       string
	AckWait       time.Duration
	MaxAckPending int
	MaxDeliver    int
	// Backoff is the NAK delay by delivery number. It is applied by the
	// router rather than through the consumer's BackOff, which would also
	// stretch AckWait and so delay the recovery from a crashed replica.
	Backoff     []time.Duration
	Concurrency int
	// Health gates fetching on the router's dependencies.
	Health HealthConfig
}

func (c ConsumerConfig) withDefaults() ConsumerConfig {
	if c.Durable == "" {
		c.Durable = DefaultConsumerDurable
	}

	if c.AckWait <= 0 {
		c.AckWait = DefaultConsumerAckWait
	}

	if c.MaxAckPending <= 0 {
		c.MaxAckPending = DefaultConsumerMaxAckPending
	}

	if c.MaxDeliver <= 0 {
		c.MaxDeliver = DefaultConsumerMaxDeliver
	}

	if len(c.Backoff) == 0 {
		c.Backoff = DefaultConsumerBackoff
	}

	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConsumerConcurrency
	}

	c.Health = c.Health.withDefaults()

	return c
}

// nakDelay returns the redelivery delay after the numDelivered-th delivery
// failed.
func (c ConsumerConfig) nakDelay(numDelivered uint64) time.Duration {
	i := int(numDelivered) - 1 //nolint:gosec // bounded by MaxDeliver
	if i < 0 {
		i = 0
	}

	if i >= len(c.Backoff) {
		i = len(c.Backoff) - 1
	}

	return c.Backoff[i]
}

// permanentError marks a failure that a redelivery cannot fix.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// permanent marks err as one a redelivery cannot fix: the event or a
// subscription is malformed, and handling it again gives the same result.
// Everything not marked is transient (Temporal, database or NATS trouble).
func permanent(err error) error {
	if err == nil {
		return nil
	}

	return &permanentError{err: err}
}

// isPermanent reports whether every failure in err is permanent. A joined
// error is permanent only if all its parts are: one transient part still
// needs a redelivery. Besides errors marked with permanent(), a Temporal
// InvalidArgument is permanent.
func isPermanent(err error) bool {
	switch e := err.(type) { //nolint:errorlint // walks the tree itself
	case nil:
		return false
	case *permanentError:
		return true
	case *serviceerror.InvalidArgument:
		// The signal count or blob size limit, a malformed request: Temporal
		// answers the same on every redelivery. (NamespaceNotFound is not
		// listed: a namespace created a moment ago is reported missing for
		// about a second.)
		return true
	case interface{ Unwrap() []error }:
		parts := e.Unwrap()
		for _, p := range parts {
			if !isPermanent(p) {
				return false
			}
		}

		return len(parts) > 0
	case interface{ Unwrap() error }:
		return isPermanent(e.Unwrap())
	default:
		return false
	}
}

// consumerRun is the running pull loop.
type consumerRun struct {
	consumer jetstream.Consumer
	gate     *healthGate
	stop     chan struct{}
	loopDone chan struct{}
	stopOnce sync.Once
}

// filterSubjects returns the subjects the consumer is filtered to. They are
// built from the topic types themselves so they cannot drift from the format
// publishers use.
func (wr *SignalRouter) filterSubjects() []string {
	return []string{
		events.MutationEventTopic{StreamName: wr.streamName}.String(),
		events.TemporalWorkflowStateChangeTopic{StreamName: wr.streamName}.String(),
	}
}

// Start creates or updates the durable consumer and starts pulling events.
//
// Call it once during startup, after the database and NATS are up. It returns
// as soon as the consumer exists; events are handled in the background by at
// most ConsumerConfig.Concurrency goroutines. Each event is acknowledged when
// every target is done, and failures are handled as follows:
//
//   - permanent (malformed event, unknown operation or signal type, bad
//     topic or filter): acknowledged, logged;
//   - transient (Temporal, database): NAK with the backoff delay;
//   - still failing on delivery MaxDeliver: acknowledged, logged at error
//     level with the failed targets, and counted in
//     workflow_signal_router_events_given_up_total.
//
// While a handler runs the router sends InProgress every AckWait/3, so a long
// fan-out is not redelivered under itself.
//
// The ctx is the parent of every handler's context. Cancelling it makes
// in-flight handlers fail and their events return with a NAK.
func (wr *SignalRouter) Start(ctx context.Context) error {
	cfg := wr.consumerCfg

	if wr.jetstreamClient == nil {
		return errors.New("signal router needs a JetStream client") //nolint:err113 // startup misconfiguration
	}

	stream := events.MutationEventTopic{StreamName: wr.streamName}.GetStreamName()

	consumer, err := wr.jetstreamClient.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:        cfg.Durable,
		FilterSubjects: wr.filterSubjects(),
		DeliverPolicy:  jetstream.DeliverNewPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        cfg.AckWait,
		MaxAckPending:  cfg.MaxAckPending,
		// No server-side limit: the server drops an event after MaxDeliver
		// deliveries without a word, and a release NAK or a crash during a
		// hold counts as a delivery. The router enforces cfg.MaxDeliver itself
		// (see process), which gives up, counts, logs and records the outcome.
		// MaxDeliver is an updatable field, so an existing durable is changed
		// in place.
		MaxDeliver: -1,
	})
	if err != nil {
		// The server refuses an update of an existing durable that differs in an
		// immutable field (DeliverPolicy, AckPolicy), for example one created by
		// hand or by another build. Failing here would crash-loop the service
		// and stop all routing, so bind the durable as it is and say so.
		var bindErr error

		consumer, bindErr = wr.jetstreamClient.Consumer(ctx, stream, cfg.Durable)
		if bindErr != nil {
			return fmt.Errorf("failed to create signal router consumer: %w", errors.Join(err, bindErr))
		}

		log.ForContext(ctx).Warn().
			Err(err).
			Str("durable", cfg.Durable).
			Msg("could not update the signal router consumer, using the existing one as it is")
	}

	run := &consumerRun{
		consumer: consumer,
		gate:     newHealthGate(cfg.Health),
		stop:     make(chan struct{}),
		loopDone: make(chan struct{}),
	}
	wr.run = run

	if run.gate.enabled() {
		go run.gate.run(ctx, run.stop)
	}

	go wr.pull(ctx, run)

	log.ForContext(ctx).Info().
		Str("service", wr.serviceName).
		Str("durable", cfg.Durable).
		Strs("filter_subjects", wr.filterSubjects()).
		Msg("workflow router started successfully")

	return nil
}

// pull fetches events one at a time and hands each to a handler goroutine. It
// fetches only when a handler slot is free and the health gate is open, so no
// event sits fetched but unhandled with its AckWait running. A request already
// waiting at the server when the gate closes may still deliver one event; that
// event is held, not handled, until the gate reopens. It returns when Stop or
// ctx ends it.
func (wr *SignalRouter) pull(ctx context.Context, run *consumerRun) {
	defer close(run.loopDone)

	slots := make(chan struct{}, wr.consumerCfg.Concurrency)
	release := func(n int) {
		for range n {
			<-slots
		}
	}

	for {
		select {
		case slots <- struct{}{}:
		case <-run.stop:
			return
		case <-ctx.Done():
			return
		}

		if !run.gate.waitOpen(ctx, run.stop) {
			release(1)
			return
		}

		select { // both the slot and the stop signal may have been ready
		case <-run.stop:
			release(1)
			return
		default:
		}

		// One event per request. A batch request stays open until it is full
		// or expires and would take events that arrive after the gate closed.
		batch, err := run.consumer.Fetch(1, jetstream.FetchMaxWait(fetchWait))
		if err != nil {
			release(1)
			wr.pullFailed(ctx, run, err)

			continue
		}

		got := 0

		for msg := range batch.Messages() {
			got++

			wr.wg.Add(1)

			go func() {
				defer release(1)

				wr.process(ctx, run, msg)
			}()
		}

		if got == 0 {
			release(1)
		}

		if err := batch.Error(); err != nil {
			wr.pullFailed(ctx, run, err)
		}
	}
}

// pullFailed logs a failed pull and waits a moment so a persistent error does
// not spin the loop.
func (wr *SignalRouter) pullFailed(ctx context.Context, run *consumerRun, err error) {
	log.ForContext(ctx).Warn().Err(err).Msg("failed to pull signal router event")

	select {
	case <-ctx.Done():
	case <-run.stop:
	case <-time.After(pullErrorPause):
	}
}

// process handles one event and settles it with the server.
//
// While a dependency is down the event is held, not settled: the router keeps
// sending InProgress and runs the handler again once the gate reopens. A NAK
// would do: the redelivery carries NumDelivered+1, so an outage would burn
// attempts and eventually give up on events that never had anything wrong
// with them. Holding costs nothing, since InProgress neither redelivers nor
// counts a delivery. If the replica dies while holding, AckWait redelivers
// the event to another one.
//
// A Temporal rate limit (ResourceExhausted) is held the same way but per
// event: CheckHealth keeps passing, so there is no outage to pause the router
// for, and a NAK would use up MaxDeliver on an event that is fine. The event
// is retried after ResumeMin, doubling to ResumeMax, for at most
// HealthConfig.RateLimitHold; after that the error counts as an ordinary
// failed delivery, so an event that can never succeed (for example a message
// the gRPC size limit refuses, which is also ResourceExhausted) cannot hold
// its MaxAckPending slot forever.
//
// A Temporal Unavailable or DeadlineExceeded while every probe passes is held
// the same way, with the same bound: Temporal's own database or history can be
// down while its frontend still answers the health check, so no probe trips
// the gate. (A failing probe pauses the router instead.)
func (wr *SignalRouter) process(ctx context.Context, run *consumerRun, msg jetstream.Msg) {
	defer wr.wg.Done()

	cfg := wr.consumerCfg
	delivered := uint64(1)

	if meta, err := msg.Metadata(); err == nil {
		delivered = meta.NumDelivered
	}

	logger := log.ForContext(ctx).With().
		Str("topic", msg.Subject()).
		Uint64("delivery", delivered).
		Logger()

	stopKeepAlive := keepAlive(msg, cfg.AckWait/3, logger)

	// release hands a held event back for another replica when the router
	// stops while holding it.
	release := func() {
		stopKeepAlive()
		logger.Info().Msg("signal router stopping, releasing held event")

		if nakErr := msg.NakWithDelay(cfg.nakDelay(1)); nakErr != nil {
			logger.Error().Err(nakErr).Msg("failed to release held signal router event, it returns after AckWait")
		}
	}

	var (
		err      error
		heldFrom time.Time // when this event was first held per event
		holdFor  = cfg.Health.ResumeMin
	)

	for {
		if !run.gate.waitOpen(ctx, run.stop) {
			release()

			return
		}

		err = wr.safeDispatch(withDelivery(events.ContextFromJetstreamMessage(ctx, msg), delivered), &nats.Msg{
			Subject: msg.Subject(),
			Data:    msg.Data(),
			Header:  msg.Headers(),
		})

		if err != nil && run.gate.confirmOutage(ctx, err) {
			logger.Warn().Err(err).Msg("signal router event hit a dependency outage, holding it until the dependency is back")

			continue
		}

		// A rate limit, or a Temporal Unavailable the probes do not see, is
		// not an outage: the probes pass, so the gate stays open and other
		// events keep flowing. Only this event waits, still un-acked, and
		// tries again after a growing delay.
		if err != nil && (isRateLimited(err) || isTemporalUnavailable(err)) {
			if heldFrom.IsZero() {
				heldFrom = time.Now()
			}

			if time.Since(heldFrom) < cfg.Health.RateLimitHold {
				logger.Warn().Err(err).Dur("retry_in", holdFor).
					Msg("signal router event was rate limited or Temporal is unavailable for it, holding it and trying again")

				if !sleepUnlessStopped(ctx, run.stop, holdFor) {
					release()

					return
				}

				holdFor = min(holdFor*2, cfg.Health.ResumeMax)

				continue
			}

			logger.Warn().Err(err).Dur("held_for", time.Since(heldFrom)).
				Msg("signal router event stayed held past the hold budget, counting it as a failed delivery")
		}

		break
	}

	stopKeepAlive()

	if outcome, settled := settlement(err, delivered, cfg.MaxDeliver); settled {
		wr.recordOutcome(ctx, msg, outcome, err)
	} else {
		wr.discardRouting(msg) // NAKed below: the redelivery collects afresh
	}

	switch {
	case err == nil:
		ack(logger, msg)
	case isPermanent(err):
		logger.Warn().Err(err).Msg("signal router event failed permanently, acknowledging")
		ack(logger, msg)
	case delivered >= uint64(cfg.MaxDeliver): //nolint:gosec // validated positive
		eventsGivenUp.Inc()
		logger.Error().Err(err).Msg("signal router gave up on event after the last delivery, acknowledging")
		ack(logger, msg)
	default:
		delay := cfg.nakDelay(delivered)
		logger.Warn().Err(err).Dur("retry_in", delay).Msg("signal router event failed, will be redelivered")

		if nakErr := msg.NakWithDelay(delay); nakErr != nil {
			logger.Error().Err(nakErr).Msg("failed to NAK signal router event, it returns after AckWait")
		}
	}
}

// sleepUnlessStopped waits d. It returns false if stop or ctx ended first.
func sleepUnlessStopped(ctx context.Context, stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-stop:
		return false
	case <-ctx.Done():
		return false
	}
}

func ack(logger zerolog.Logger, msg jetstream.Msg) {
	if err := msg.Ack(); err != nil {
		logger.Error().Err(err).Msg("failed to acknowledge signal router event, it will be redelivered")
	}
}

// safeDispatch turns a handler panic into a (transient) error so that one
// event cannot take the replica down.
func (wr *SignalRouter) safeDispatch(ctx context.Context, msg *nats.Msg) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("signal router handler panicked: %v", r) //nolint:err113 // wraps a panic value
		}
	}()

	return wr.dispatch(ctx, msg)
}

// keepAlive sends InProgress every interval until the returned func is called.
// A failed InProgress is logged: the event may then be redelivered after AckWait.
func keepAlive(msg jetstream.Msg, interval time.Duration, logger zerolog.Logger) func() {
	done := make(chan struct{})

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()

		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := msg.InProgress(); err != nil {
					logger.Warn().Err(err).Msg("failed to extend the ack deadline of a signal router event, it may be redelivered")
				}
			}
		}
	}()

	return func() { close(done) }
}

// systemNamespaceStateChange reports whether subject is a Temporal state change
// of a namespace that is not a tenant, such as default or temporal-system, and
// returns that namespace. The subject is recognised by shape: the stream and
// task queue tokens are free, the third token is the literal "temporal" and
// the namespace token is neither the wildcard "*" nor a UUID, which is exactly
// what events.Parse rejects for this pattern. Any other subject, including a
// state change of a tenant UUID, is not one.
func systemNamespaceStateChange(subject string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(subject), ".")
	if len(parts) != stateChangeSubjectTokens || parts[2] != "temporal" {
		return "", false
	}

	namespace := parts[1]
	if namespace == "*" {
		return "", false
	}

	if _, err := uuid.Parse(namespace); err == nil {
		return "", false
	}

	return namespace, true
}

// dispatchEvent routes one event to its handler by subject.
//
// A state change of a system namespace is skipped before the subject is
// parsed: events.Parse rejects it (its namespace is not a tenant UUID), which
// would make it a permanent failure logged at WARN, for events that are
// expected and have no tenant to route to. It returns nil, so the event is
// acknowledged as handled; settle records nothing for it, since routing
// statuses only exist for mutation events.
func (wr *SignalRouter) dispatchEvent(ctx context.Context, msg *nats.Msg) error {
	if namespace, ok := systemNamespaceStateChange(msg.Subject); ok {
		log.ForContext(ctx).Debug().
			Str("subject", msg.Subject).
			Str("namespace", namespace).
			Msg("namespace is not a tenant UUID, skipping state change (expected for system namespaces)")

		return nil
	}

	topic, err := events.Parse(msg.Subject)
	if err != nil {
		return permanent(fmt.Errorf("failed to parse event topic %q: %w", msg.Subject, err))
	}

	switch topic.Type() { //nolint:exhaustive // only two types are consumed
	case events.TopicTypeMutationEvent:
		_, err = wr.HandleMutationEvent(ctx, msg)
	case events.TopicTypeTemporalWorkflowStateChangeEvent:
		_, err = wr.handleTemporalWorkflowStateChange(ctx, msg)
	default:
		return permanent(fmt.Errorf("%w: unexpected topic %q", ErrInvalidEventMessage, msg.Subject))
	}

	return err
}

// Stop stops pulling, lets the handlers already running finish and settle
// their events, then closes the Temporal clients. Events held because a
// dependency is down are released back to the stream instead of waited on.
// Stop returns within about fetchWait of the last handler finishing. Safe to
// call more than once.
func (wr *SignalRouter) Stop() {
	if run := wr.run; run != nil {
		run.stopOnce.Do(func() { close(run.stop) })
		<-run.loopDone
	}

	// Wait for all in-flight requests to complete
	wr.wg.Wait()

	if wr.clientFactory != nil {
		wr.clientFactory.Close()
	}
}
