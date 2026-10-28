package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/events/topic"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// Event streams, named by the topic package:
//
//	<prefix>-<tenantID>  a tenant's events, subjects <prefix>.<tenantID>.>
//	<prefix>-system      events of no tenant, subjects <prefix>-system.>
//	<prefix>             aggregate for the services' consumers: no subjects,
//	                     sources every stream above
//
// Duplicates are detected per stream, so a tenant's Nats-Msg-Id can suppress
// only messages in its own stream. The aggregate detects none.
//
// Every event is stored twice, in its stream and in the aggregate. The caps
// (StreamLimits) count messages and discard the oldest, so one tenant that
// publishes fast enough pushes other tenants' unread events, lifecycle and
// revocation events included, out of the aggregate before a consumer reads
// them. They are not byte caps: the server reserves a stream's
// MaxBytes in the file store when the stream is created, so byte caps would
// refuse tenants long before the store is full.
const (
	streamMaxConsumers = 200

	// aggregateMaxAckPending bounds the unacknowledged messages of each
	// consumer of the aggregate.
	aggregateMaxAckPending = 10_000

	// defaultTenantStreamLockWait bounds EnsureTenant's and DeleteTenant's
	// wait for the lock, which a Reconcile holds while it puts every tenant
	// stream.
	defaultTenantStreamLockWait = 10 * time.Second

	// defaultStreamReconcileConcurrency bounds the parallel stream writes of
	// Reconcile and RepairTenants.
	defaultStreamReconcileConcurrency = 32

	// aggregateWarnRatio is the share of max_payload past which the aggregate's
	// size is logged.
	aggregateWarnRatio = 0.8

	// tenantErrorsShown is how many per-tenant failures an ErrTenantStreams
	// error spells out; the rest are counted.
	tenantErrorsShown = 5
)

// StreamLimits bound the event streams; StreamLimitsOf reads them from the
// configuration. Management's StreamTopology puts them; the other services use
// them only to create a missing system stream or aggregate (SetupJetStream), so
// differing values do not overwrite each other.
type StreamLimits struct {
	MaxAge           time.Duration
	TenantMaxMsgs    int64
	SystemMaxMsgs    int64
	AggregateMaxMsgs int64
}

// StreamLimitsOf returns the stream limits cfg configures.
func StreamLimitsOf(cfg config.NatsConfig) StreamLimits {
	return StreamLimits{
		MaxAge:           cfg.NatsStreamMaxAge,
		TenantMaxMsgs:    cfg.NatsTenantStreamMaxMsgs,
		SystemMaxMsgs:    cfg.NatsSystemStreamMaxMsgs,
		AggregateMaxMsgs: cfg.NatsAggregateStreamMaxMsgs,
	}
}

// streamSettings are what the StreamOptions set.
type streamSettings struct {
	lockWait             time.Duration
	reconcileConcurrency int
}

// StreamOption changes a setting of NewStreamTopology.
type StreamOption func(*streamSettings)

// WithLockWait bounds how long EnsureTenant and DeleteTenant wait for the
// lock.
func WithLockWait(d time.Duration) StreamOption {
	return func(s *streamSettings) { s.lockWait = d }
}

// WithReconcileConcurrency bounds the parallel stream writes of Reconcile and
// RepairTenants; n must be positive.
func WithReconcileConcurrency(n int) StreamOption {
	return func(s *streamSettings) { s.reconcileConcurrency = n }
}

func newStreamSettings(opts []StreamOption) streamSettings {
	settings := streamSettings{
		lockWait:             defaultTenantStreamLockWait,
		reconcileConcurrency: defaultStreamReconcileConcurrency,
	}
	for _, opt := range opts {
		opt(&settings)
	}

	return settings
}

var (
	// ErrNilTenantID is returned for the nil tenant ID, which has no stream.
	ErrNilTenantID = errors.New("the nil tenant ID has no stream")

	// ErrAggregateTooLarge is returned when the aggregate's configuration no
	// longer fits max_payload. The aggregate is left as it was.
	ErrAggregateTooLarge = errors.New("the aggregate stream's configuration exceeds max_payload")

	// ErrStreamLockNotAcquired is returned by EnsureTenant and DeleteTenant
	// when they did not get the lock. Nothing was changed.
	ErrStreamLockNotAcquired = errors.New("event stream lock not acquired")

	// ErrTenantStreams wraps the per-tenant failures of Reconcile and
	// RepairTenants.
	ErrTenantStreams = errors.New("put tenant streams")

	// ErrInvalidStreamMaxAge is returned by SetupJetStream for a max age other
	// than 0 (no limit) below StreamDuplicateWindow: the server refuses a
	// duplicate window larger than the max age, on every stream.
	ErrInvalidStreamMaxAge = errors.New("stream max age must be 0 or at least the duplicate window")

	// ErrInvalidStreamMaxMsgs is returned by SetupJetStream for a message cap
	// of 0 or less, which the server would store as no cap.
	ErrInvalidStreamMaxMsgs = errors.New("stream max messages must be positive")
)

func baseStreamConfig(name, description string, replicas int, maxAge time.Duration, maxMsgs int64) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:         name,
		Description:  description,
		Replicas:     replicas,
		MaxConsumers: streamMaxConsumers,
		MaxAge:       maxAge,
		MaxMsgs:      maxMsgs,
		MaxBytes:     -1, // reserves nothing (see above)
		Duplicates:   StreamDuplicateWindow,
		Retention:    jetstream.LimitsPolicy,
		Discard:      jetstream.DiscardOld,
		ConsumerLimits: jetstream.StreamConsumerLimits{
			InactiveThreshold: maxAge,
		},
	}
}

func tenantStreamConfig(prefix string, replicas int, limits StreamLimits, tenantID uuid.UUID) jetstream.StreamConfig {
	cfg := baseStreamConfig(topic.TenantStreamName(prefix, tenantID), "Events of tenant "+tenantID.String(), replicas, limits.MaxAge, limits.TenantMaxMsgs)
	cfg.Subjects = []string{topic.TenantStreamSubjects(prefix, tenantID)}

	return cfg
}

func systemStreamConfig(prefix string, replicas int, limits StreamLimits) jetstream.StreamConfig {
	cfg := baseStreamConfig(topic.SystemStreamName(prefix), "Events of no tenant", replicas, limits.MaxAge, limits.SystemMaxMsgs)
	cfg.Subjects = []string{topic.SystemStreamSubjects(prefix)}

	return cfg
}

func aggregateStreamConfig(prefix string, replicas int, limits StreamLimits, sources []string) jetstream.StreamConfig {
	cfg := baseStreamConfig(topic.AggregateStreamName(prefix), "Events of every tenant and the system", replicas, limits.MaxAge, limits.AggregateMaxMsgs)

	// Off, so one source cannot suppress another's message. The server keeps a
	// zero window only for a stream with sources.
	cfg.Duplicates = 0

	// Not on the sourced streams: there it invalidates the server's source
	// consumer, which acknowledges nothing, and sourcing silently stops.
	cfg.ConsumerLimits.MaxAckPending = aggregateMaxAckPending

	cfg.Sources = make([]*jetstream.StreamSource, len(sources))
	for i, name := range sources {
		cfg.Sources[i] = &jetstream.StreamSource{Name: name}
	}

	return cfg
}

// SetupJetStream returns a JetStream client on nc and creates the system stream
// and the aggregate if they are missing. Existing ones are left to the
// management service's StreamTopology, which owns their limits and the
// aggregate's sources. A max age the server would refuse fails with
// ErrInvalidStreamMaxAge, a message cap of 0 or less with
// ErrInvalidStreamMaxMsgs.
//
//nolint:ireturn // jetstream.JetStream is the client the callers need.
func SetupJetStream(ctx context.Context, nc *nats.Conn, prefix string, replicas int, limits StreamLimits) (jetstream.JetStream, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}

	if limits.MaxAge < 0 || (limits.MaxAge > 0 && limits.MaxAge < StreamDuplicateWindow) {
		return nil, fmt.Errorf("invalid PYCK_NATS_STREAM_MAX_AGE value %s: %w (%s)", limits.MaxAge, ErrInvalidStreamMaxAge, StreamDuplicateWindow)
	}

	for _, limit := range []struct {
		name string
		n    int64
	}{
		{"PYCK_NATS_TENANT_STREAM_MAX_MSGS", limits.TenantMaxMsgs},
		{"PYCK_NATS_SYSTEM_STREAM_MAX_MSGS", limits.SystemMaxMsgs},
		{"PYCK_NATS_AGGREGATE_STREAM_MAX_MSGS", limits.AggregateMaxMsgs},
	} {
		if limit.n <= 0 {
			return nil, fmt.Errorf("invalid %s value %d: %w", limit.name, limit.n, ErrInvalidStreamMaxMsgs)
		}
	}

	system := systemStreamConfig(prefix, replicas, limits)
	if _, err := js.CreateStream(ctx, system); err != nil && !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		return nil, fmt.Errorf("set up the system stream: %w", err)
	}

	aggregate := aggregateStreamConfig(prefix, replicas, limits, []string{topic.SystemStreamName(prefix)})
	if _, err := js.CreateStream(ctx, aggregate); err != nil && !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		return nil, fmt.Errorf("set up the aggregate stream: %w", err)
	}

	return js, nil
}

// StreamLock runs fn exclusively among all callers of the lock, across
// processes. It gives up waiting when ctx ends, but once fn runs it holds the
// lock until fn returns.
type StreamLock func(ctx context.Context, fn func(context.Context) error) error

// StreamTopology manages the tenant streams and the aggregate's sources. Every
// method runs under the lock: the source list is written whole, so concurrent
// writers would drop each other's sources.
type StreamTopology struct {
	js       jetstream.JetStream
	prefix   string
	replicas int
	lock     StreamLock
	limits   StreamLimits
	settings streamSettings
}

// NewStreamTopology returns the topology of the streams of prefix.
func NewStreamTopology(js jetstream.JetStream, prefix string, replicas int, lock StreamLock, limits StreamLimits, opts ...StreamOption) *StreamTopology {
	return &StreamTopology{js: js, prefix: prefix, replicas: replicas, lock: lock, limits: limits, settings: newStreamSettings(opts)}
}

// EnsureTenant puts tenantID's stream and has the aggregate source it. Until
// then the tenant's events cannot be published. It waits for the lock at most
// the lock wait (WithLockWait).
//
// On failure it removes the stream if this call created it, even after ctx is
// cancelled; a stream that existed before is kept.
func (t *StreamTopology) EnsureTenant(ctx context.Context, tenantID uuid.UUID) error {
	if tenantID == uuid.Nil {
		return ErrNilTenantID
	}

	waitCtx, cancel := context.WithTimeout(ctx, t.settings.lockWait)
	defer cancel()

	ran := false
	err := t.lock(waitCtx, func(context.Context) error {
		ran = true

		// ctx, not waitCtx: only the wait is bounded.
		created, err := putStream(ctx, t.js, tenantStreamConfig(t.prefix, t.replicas, t.limits, tenantID))
		if err != nil {
			err = fmt.Errorf("put stream of tenant %s: %w", tenantID, err)
		} else {
			err = t.syncAggregate(ctx)
		}

		// A source the failed sync may have added is dropped by the next one.
		if err != nil && created {
			err = errors.Join(err, t.deleteTenantStream(context.WithoutCancel(ctx), tenantID))
		}

		return err
	})
	if !ran {
		return fmt.Errorf("%w: %w", ErrStreamLockNotAcquired, err)
	}

	return err
}

// DeleteTenant removes tenantID's stream and purges its events from the
// aggregate. Only for a hard delete (#1291) or a registration whose row was
// not inserted: a soft-deleted tenant keeps its stream. It waits for the lock
// at most the lock wait (WithLockWait).
func (t *StreamTopology) DeleteTenant(ctx context.Context, tenantID uuid.UUID) error {
	if tenantID == uuid.Nil {
		return ErrNilTenantID
	}

	waitCtx, cancel := context.WithTimeout(ctx, t.settings.lockWait)
	defer cancel()

	ran := false
	err := t.lock(waitCtx, func(context.Context) error {
		ran = true

		// ctx, not waitCtx: only the wait is bounded.
		if err := t.deleteTenantStream(ctx, tenantID); err != nil {
			return err
		}

		if err := t.syncAggregate(ctx); err != nil {
			return err
		}

		aggregate, err := t.js.Stream(ctx, topic.AggregateStreamName(t.prefix))
		if err != nil {
			return fmt.Errorf("look up the aggregate stream: %w", err)
		}

		if err := aggregate.Purge(ctx, jetstream.WithPurgeSubject(topic.TenantStreamSubjects(t.prefix, tenantID))); err != nil {
			return fmt.Errorf("purge events of tenant %s from the aggregate: %w", tenantID, err)
		}

		return nil
	})
	if !ran {
		return fmt.Errorf("%w: %w", ErrStreamLockNotAcquired, err)
	}

	return err
}

// deleteTenantStream deletes tenantID's stream; a missing one is not an error.
func (t *StreamTopology) deleteTenantStream(ctx context.Context, tenantID uuid.UUID) error {
	err := t.js.DeleteStream(ctx, topic.TenantStreamName(t.prefix, tenantID))
	if err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
		return fmt.Errorf("delete stream of tenant %s: %w", tenantID, err)
	}

	return nil
}

// Reconcile puts the system stream, a stream for each of tenantIDs, and the
// aggregate. It never deletes a tenant stream.
//
// Tenant streams that cannot be put (ErrTenantStreams) and an aggregate past
// max_payload (ErrAggregateTooLarge) are partial failures: every stream that
// exists is still sourced. Any other error is returned alone.
//
// It also migrates the shared stream of the previous layout in place: the
// first sync drops its subjects, so the tenant streams can capture them, while
// its messages and consumers stay. Publishes in between fail and are retried.
func (t *StreamTopology) Reconcile(ctx context.Context, tenantIDs []uuid.UUID) (StreamRepair, error) {
	var repair StreamRepair

	err := t.lock(ctx, func(ctx context.Context) error {
		// Past max_payload the tenant streams are still put, below.
		if err := t.syncAggregate(ctx); err != nil && !errors.Is(err, ErrAggregateTooLarge) {
			return err
		}

		var tenantErr error
		repair.Failed, tenantErr = t.putTenantStreams(ctx, tenantIDs)

		err := t.syncAggregateCounting(ctx, &repair)
		if err != nil && !errors.Is(err, ErrAggregateTooLarge) {
			return err
		}

		return errors.Join(err, tenantErr)
	})

	return repair, err
}

// StreamRepair counts the tenant streams that could not be put, those the
// aggregate does not source (past max_payload) and, for RepairTenants, those
// that were missing.
type StreamRepair struct {
	Missing   int
	Failed    int
	Unsourced int
}

// RepairTenants puts the missing streams of tenantIDs and syncs the aggregate,
// leaving existing streams as they are. Reconcile runs at startup only; this
// brings back a stream lost later. Failures are partial as in Reconcile.
func (t *StreamTopology) RepairTenants(ctx context.Context, tenantIDs []uuid.UUID) (StreamRepair, error) {
	var repair StreamRepair

	err := t.lock(ctx, func(ctx context.Context) error {
		absent, err := t.missingTenants(ctx, tenantIDs)
		if err != nil {
			return err
		}

		var tenantErr error
		repair.Missing = len(absent)
		repair.Failed, tenantErr = t.putTenantStreams(ctx, absent)

		err = t.syncAggregateCounting(ctx, &repair)
		if err != nil && !errors.Is(err, ErrAggregateTooLarge) {
			return err
		}

		return errors.Join(err, tenantErr)
	})

	return repair, err
}

// missingTenants returns the tenants of tenantIDs that have no stream.
func (t *StreamTopology) missingTenants(ctx context.Context, tenantIDs []uuid.UUID) ([]uuid.UUID, error) {
	names, err := t.tenantStreamNames(ctx)
	if err != nil {
		return nil, err
	}

	var missing []uuid.UUID

	for _, tenantID := range tenantIDs {
		if tenantID == uuid.Nil {
			continue
		}
		if _, found := slices.BinarySearch(names, topic.TenantStreamName(t.prefix, tenantID)); !found {
			missing = append(missing, tenantID)
		}
	}

	return missing, nil
}

// putTenantStreams puts the streams of tenantIDs, the reconcile concurrency at
// a time, and returns how many failed. The error names only the first few:
// with NATS degraded, every tenant can fail.
func (t *StreamTopology) putTenantStreams(ctx context.Context, tenantIDs []uuid.UUID) (int, error) {
	var wg sync.WaitGroup

	slots := make(chan struct{}, t.settings.reconcileConcurrency)
	errs := make([]error, len(tenantIDs))

	for i, tenantID := range tenantIDs {
		if tenantID == uuid.Nil {
			continue
		}

		slots <- struct{}{}

		wg.Go(func() {
			defer func() { <-slots }()

			if _, err := putStream(ctx, t.js, tenantStreamConfig(t.prefix, t.replicas, t.limits, tenantID)); err != nil {
				errs[i] = fmt.Errorf("put stream of tenant %s: %w", tenantID, err)
			}
		})
	}

	wg.Wait()

	var failed []error

	for _, err := range errs {
		if err != nil {
			failed = append(failed, err)
		}
	}

	if len(failed) == 0 {
		return 0, nil
	}

	shown := failed[:min(len(failed), tenantErrorsShown)]

	return len(failed), fmt.Errorf("%w: %d of %d failed, the first %d: %w", ErrTenantStreams, len(failed), len(tenantIDs), len(shown), errors.Join(shown...))
}

// syncAggregateCounting syncs the aggregate and, past max_payload, counts the
// tenant streams it then does not source into repair.Unsourced.
func (t *StreamTopology) syncAggregateCounting(ctx context.Context, repair *StreamRepair) error {
	err := t.syncAggregate(ctx)
	if !errors.Is(err, ErrAggregateTooLarge) {
		return err
	}

	unsourced, countErr := t.unsourcedTenantStreams(ctx)
	repair.Unsourced = unsourced

	return errors.Join(err, countErr)
}

// unsourcedTenantStreams counts the tenant streams the aggregate does not source.
func (t *StreamTopology) unsourcedTenantStreams(ctx context.Context) (int, error) {
	names, err := t.tenantStreamNames(ctx)
	if err != nil {
		return 0, err
	}

	sourced := make(map[string]bool)

	aggregate, err := t.js.Stream(ctx, topic.AggregateStreamName(t.prefix))
	switch {
	case errors.Is(err, jetstream.ErrStreamNotFound):
	case err != nil:
		return 0, fmt.Errorf("look up the aggregate stream: %w", err)
	default:
		for _, source := range aggregate.CachedInfo().Config.Sources {
			sourced[source.Name] = true
		}
	}

	unsourced := 0
	for _, name := range names {
		if !sourced[name] {
			unsourced++
		}
	}

	return unsourced, nil
}

// syncAggregate has the aggregate source the system stream and every existing
// tenant stream. It puts the system stream first: the aggregate may be created
// here, and sourcing a missing stream fails silently.
func (t *StreamTopology) syncAggregate(ctx context.Context) error {
	if _, err := putStream(ctx, t.js, systemStreamConfig(t.prefix, t.replicas, t.limits)); err != nil {
		return fmt.Errorf("put the system stream: %w", err)
	}

	names, err := t.tenantStreamNames(ctx)
	if err != nil {
		return err
	}

	sources := append([]string{topic.SystemStreamName(t.prefix)}, names...)
	cfg := aggregateStreamConfig(t.prefix, t.replicas, t.limits, sources)

	if err := t.checkAggregateSize(ctx, cfg); err != nil {
		return err
	}

	if _, err := putStream(ctx, t.js, cfg); err != nil {
		return fmt.Errorf("put the aggregate stream: %w", err)
	}

	return nil
}

// checkAggregateSize fails before the server would: the create or update
// request carries every source and must fit max_payload.
func (t *StreamTopology) checkAggregateSize(ctx context.Context, cfg jetstream.StreamConfig) error {
	limit := t.js.Conn().MaxPayload()
	if limit <= 0 {
		return nil
	}

	encoded, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode the aggregate stream: %w", err)
	}

	size := int64(len(encoded))
	if size > limit {
		return fmt.Errorf("%w: %d sources take %d of %d bytes", ErrAggregateTooLarge, len(cfg.Sources), size, limit)
	}

	if float64(size) >= aggregateWarnRatio*float64(limit) {
		log.ForContext(ctx).Warn().
			Int("sources", len(cfg.Sources)).
			Int64("bytes", size).
			Int64("max_payload", limit).
			Msg("the aggregate stream nears max_payload; raise it before tenants can no longer be added")
	}

	return nil
}

// tenantStreamNames lists names only: a page of full stream infos can exceed
// the server's max_payload.
func (t *StreamTopology) tenantStreamNames(ctx context.Context) ([]string, error) {
	var names []string

	lister := t.js.StreamNames(ctx)
	for name := range lister.Name() {
		if _, ok := topic.ParseTenantStreamName(t.prefix, name); ok {
			names = append(names, name)
		}
	}

	if err := lister.Err(); err != nil {
		return nil, fmt.Errorf("list stream names: %w", err)
	}

	slices.Sort(names)

	return names, nil
}

// putStream creates the stream, or updates it only when it differs from cfg.
// It reports whether it tried to create the stream, also on error: a failed
// create may have landed.
func putStream(ctx context.Context, js jetstream.JetStream, cfg jetstream.StreamConfig) (bool, error) {
	stream, err := js.Stream(ctx, cfg.Name)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		_, err = js.CreateStream(ctx, cfg)
		if !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
			return true, err
		}

		// Created concurrently with other settings.
		stream, err = js.Stream(ctx, cfg.Name)
	}
	if err != nil {
		return false, err
	}

	if sameStreamConfig(cfg, stream.CachedInfo().Config) {
		return false, nil
	}

	_, err = js.UpdateStream(ctx, cfg)

	return false, err
}

// sameStreamConfig compares the settings the configs above make; the rest are
// server defaults.
func sameStreamConfig(want, have jetstream.StreamConfig) bool {
	return want.Description == have.Description &&
		slices.Equal(want.Subjects, have.Subjects) &&
		slices.Equal(sourceNames(want.Sources), sourceNames(have.Sources)) &&
		want.Replicas == have.Replicas &&
		want.MaxConsumers == have.MaxConsumers &&
		want.MaxAge == have.MaxAge &&
		want.MaxMsgs == have.MaxMsgs &&
		want.MaxBytes == have.MaxBytes &&
		want.Duplicates == have.Duplicates &&
		want.Retention == have.Retention &&
		want.Discard == have.Discard &&
		want.ConsumerLimits == have.ConsumerLimits
}

func sourceNames(sources []*jetstream.StreamSource) []string {
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.Name)
	}

	return names
}
