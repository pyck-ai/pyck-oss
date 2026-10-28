package config

import (
	"time"

	"github.com/pyck-ai/pyck/backend/common/typing"
)

type HTTPConfig struct {
	HTTPHost              string        `env:"PYCK_HTTP_HOST" envDefault:""`
	HTTPPort              int           `env:"PYCK_HTTP_PORT,notEmpty" envDefault:"8080"`
	HTTPReadHeaderTimeout time.Duration `env:"PYCK_HTTP_READ_HEADER_TIMEOUT,notEmpty" envDefault:"5s"`

	// HTTPShutdownDrainTimeout caps how long shutdown waits for in-flight
	// requests after the listener closes; requests still running at the
	// deadline are abandoned.
	HTTPShutdownDrainTimeout time.Duration `env:"PYCK_HTTP_SHUTDOWN_DRAIN_TIMEOUT,notEmpty" envDefault:"10s"`

	// HTTPShutdownPreDrainDelay is how long the server keeps serving after
	// the stop signal, with readiness already 503, so orchestrators stop
	// routing before the listener closes. The container stop grace period
	// must exceed delay + drain timeout + resource teardown; the full
	// worst-case budget lives in docs/graceful-shutdown.md.
	HTTPShutdownPreDrainDelay time.Duration `env:"PYCK_HTTP_SHUTDOWN_PREDRAIN_DELAY,notEmpty" envDefault:"3s"`
}

type EnvironmentConfig struct {
	EnvironmentName string `env:"PYCK_ENV,notEmpty,required"`
}

type LogConfig struct {
	LogLevel  typing.LogLevel `env:"PYCK_LOG_LEVEL,notEmpty" envDefault:"info"`
	LogFormat string          `env:"PYCK_LOG_FORMAT,notEmpty" envDefault:"json"`
}

type DbConfig struct {
	DbMasterUrl string `env:"PYCK_DATABASE_MASTER_URL,notEmpty,required" json:"-"`
	DbSlaveUrl  string `env:"PYCK_DATABASE_SLAVE_URL,notEmpty,required" json:"-"`
	DbDebug     bool   `env:"PYCK_DATABASE_DEBUG,notEmpty" envDefault:"false"`
	DbDriver    string `env:"PYCK_DATABASE_DRIVER,notEmpty" envDefault:"postgres"`

	TxRetries int `env:"PYCK_TX_RETRIES,notEmpty" envDefault:"50"`

	MigrationShadowDbName string `env:"PYCK_MIGRATION_SHADOW_DB_NAME,notEmpty" envDefault:"pyck_shadow"`
	MigrationDbName       string `env:"PYCK_MIGRATION_DB_NAME,notEmpty" envDefault:"pyck_dev"`
}

type GatewayConfig struct {
	GatewayUrl string `env:"PYCK_GATEWAY_URL,notEmpty,required"`

	// GatewayHTTPTimeout caps an entire outbound gateway/management API call
	// (dial, headers, body). Calls made from resolvers ride request contexts
	// that deliberately carry no deadline, so without this a black-holed
	// gateway would pin them indefinitely; responses are small GraphQL
	// payloads, so a total cap is safe.
	GatewayHTTPTimeout time.Duration `env:"PYCK_GATEWAY_HTTP_TIMEOUT,notEmpty" envDefault:"30s"`

	// GatewayHTTPMaxIdleConns sizes the outbound client's idle connection
	// pool for the gateway host. A reuse pool, not a concurrency cap:
	// bursts beyond it run on short-lived extra connections. The stdlib
	// per-host default (2) redials constantly under concurrent resolver
	// traffic, while large values pin needless sockets on the gateway
	// across every service replica; 16 covers realistic bursts with a
	// small descriptor footprint.
	GatewayHTTPMaxIdleConns int `env:"PYCK_GATEWAY_HTTP_MAX_IDLE_CONNS,notEmpty" envDefault:"16"`
}

type TemporalConfig struct {
	TemporalUrl string `env:"PYCK_TEMPORAL_URL,notEmpty,required"`

	// TemporalDialTimeout bounds the eager connectivity check when the service
	// dials the Temporal frontend at startup.
	TemporalDialTimeout time.Duration `env:"PYCK_TEMPORAL_DIAL_TIMEOUT,notEmpty" envDefault:"30s"`

	// TemporalClientCreationTimeout bounds the lazy one-time namespace setup
	// (namespace creation + search-attribute registration) triggered by the
	// first request for a tenant namespace.
	TemporalClientCreationTimeout time.Duration `env:"PYCK_TEMPORAL_CLIENT_CREATION_TIMEOUT,notEmpty" envDefault:"30s"`
}

type TemporalBootstrapConfig struct {
	TemporalBootstrapUrl string `env:"PYCK_TEMPORAL_BOOTSTRAP_URL"`
}

type ServiceConfig struct {
	ServiceToken       string `env:"PYCK_SERVICE_TOKEN,notEmpty,required" json:"-"`
	StrictVersionCheck bool   `env:"PYCK_STRICT_VERSION_CHECK,notEmpty" envDefault:"true"`
}

type ServiceInstanceConfig struct {
	ServiceInstanceID string `env:"PYCK_SERVICE_INSTANCE_ID,notEmpty" envDefault:"default"`
}

type ZitadelConfig struct {
	ZitadelOAuthURL           string        `env:"PYCK_ZITADEL_OAUTH_URL,notEmpty,required"`
	ZitadelGrpcAddr           string        `env:"PYCK_ZITADEL_GRPC_ADDR,notEmpty,required"`
	ZitadelAudience           string        `env:"PYCK_ZITADEL_AUDIENCE,notEmpty,required"`
	ZitadelOrganizationId     string        `env:"PYCK_ZITADEL_ORG_ID,notEmpty,required"`
	ZitadelProjectId          string        `env:"PYCK_ZITADEL_PROJECT_ID,notEmpty,required"`
	ZitadelAppKeyPath         string        `env:"PYCK_ZITADEL_APP_KEYFILE,notEmpty,required"`
	ZitadelTlsInsecure        bool          `env:"PYCK_ZITADEL_TLS_INSECURE,notEmpty" envDefault:"false"`
	ZitadelPATCacheTTL        time.Duration `env:"PYCK_ZITADEL_PAT_CACHE_TTL,notEmpty" envDefault:"1h"`
	ZitadelPATCacheTTLOverlap time.Duration `env:"PYCK_ZITADEL_PAT_CACHE_TTL_OVERLAP,notEmpty" envDefault:"1m"`

	LivenessConfig
}

// LivenessConfig configures the tenant-liveness cache every service builds
// with liveness.NewCacheFromConfig.
type LivenessConfig struct {
	// LivenessCacheTTL bounds how long a tenant-liveness verdict (either
	// sign, one per tenant, shared by the auth path's home-tenant check
	// and the middleware's acting set) is reused before its source is
	// consulted again. Lifecycle events evict earlier; this is the
	// worst-case staleness when such an event is lost, in both directions.
	LivenessCacheTTL time.Duration `env:"PYCK_LIVENESS_CACHE_TTL,notEmpty" envDefault:"1m"`

	// LivenessLookupTimeout bounds how long ONE lookup waits on a request
	// path for a verdict it does not have cached; it must stay below the
	// patience of every caller, below NatsCalloutTimeout and below the
	// Temporal server's PYCK_TEMPORAL_CLAIMS_TIMEOUT.
	LivenessLookupTimeout time.Duration `env:"PYCK_LIVENESS_LOOKUP_TIMEOUT,notEmpty" envDefault:"1s"`

	// LivenessSourceTimeout bounds the detached call to the liveness
	// source that a cache miss starts; nothing else would ever cancel it.
	LivenessSourceTimeout time.Duration `env:"PYCK_LIVENESS_SOURCE_TIMEOUT,notEmpty" envDefault:"10s"`
}

type NatsConfig struct {
	NatsUrl            string `env:"PYCK_NATS_URL,notEmpty,required" json:"-"`
	NatsStreamName     string `env:"PYCK_NATS_STREAM_NAME,notEmpty,required"`
	NatsWsUrl          string `env:"PYCK_NATS_WS_URL,notEmpty,required"`
	NatsReplicasNumber int    `env:"PYCK_NATS_REPLICAS_NO,notEmpty,required"`

	// NatsTenantAccountName is the NATS account the auth callout places
	// tenant clients in. It must not be the services' account, which holds
	// the streams (config/nats/nats.conf).
	NatsTenantAccountName string `env:"PYCK_NATS_TENANT_ACCOUNT_NAME,notEmpty" envDefault:"pyck-tenants"`

	// The limits of the event streams (events.StreamLimits). Management's
	// values apply; the other services use theirs only to create a missing
	// system stream or aggregate. The caps count messages, oldest discarded.
	// The max age is 0 (no limit) or at least the 2m duplicate window
	// (events.StreamDuplicateWindow).
	NatsStreamMaxAge           time.Duration `env:"PYCK_NATS_STREAM_MAX_AGE,notEmpty" envDefault:"72h"`
	NatsTenantStreamMaxMsgs    int64         `env:"PYCK_NATS_TENANT_STREAM_MAX_MSGS,notEmpty" envDefault:"100000"`
	NatsSystemStreamMaxMsgs    int64         `env:"PYCK_NATS_SYSTEM_STREAM_MAX_MSGS,notEmpty" envDefault:"200000"`
	NatsAggregateStreamMaxMsgs int64         `env:"PYCK_NATS_AGGREGATE_STREAM_MAX_MSGS,notEmpty" envDefault:"2000000"`

	// NatsCalloutTimeout bounds one whole auth callout. It must stay below
	// the NATS server's own authorization timeout (config/nats/nats.conf)
	// and above LivenessLookupTimeout, which runs inside it.
	NatsCalloutTimeout time.Duration `env:"PYCK_NATS_CALLOUT_TIMEOUT,notEmpty" envDefault:"2s"`

	// NatsUserJWTTTL is the life of a tenant user's minted NATS
	// credential, and with it the worst-case delay before a disabled
	// tenant's already-connected client is re-evaluated. Each credential's
	// expiry is spread by up to ±1 min around it (±TTL/4 when the TTL is
	// below 4 min), so a cohort admitted together does not expire together.
	NatsUserJWTTTL time.Duration `env:"PYCK_NATS_USER_JWT_TTL,notEmpty" envDefault:"5m"`
}

// TenantRegistrationConfig bounds the registerTenant mutation: how many of
// them one replica admits at a time, how long one waits for its
// registration run, and how often it re-asks Temporal for a run that the
// trigger has not started yet.
type TenantRegistrationConfig struct {
	// RegisterTenantMaxInFlight caps concurrent registerTenant executions
	// per replica; the mutation is anonymous, so this is what bounds how
	// much provisioning work one caller can start at once.
	RegisterTenantMaxInFlight int `env:"PYCK_REGISTER_TENANT_MAX_INFLIGHT,notEmpty" envDefault:"8"`

	// RegistrationWait bounds the resolver's whole wait for the
	// registration run; it must stay under the gateway's per-subgraph
	// request timeout or the outcome never reaches the client. Management
	// refuses to start unless it is positive.
	RegistrationWait time.Duration `env:"PYCK_TENANT_REGISTRATION_WAIT,notEmpty" envDefault:"25s"`

	// RegistrationPoll is how often the resolver re-asks Temporal for a
	// run that does not exist yet while the row's create event travels.
	// Management refuses to start unless it is positive and below
	// RegistrationWait.
	RegistrationPoll time.Duration `env:"PYCK_TENANT_REGISTRATION_POLL,notEmpty" envDefault:"250ms"`
}

// TenantEventsConfig bounds the tenant-lifecycle trigger consumer that
// turns tenant CRUD events into lifecycle workflow starts.
type TenantEventsConfig struct {
	// TenantEventMaxRedeliver caps the deliveries (the first included) of
	// one tenant event before JetStream drops it.
	TenantEventMaxRedeliver int `env:"PYCK_TENANT_EVENT_MAX_REDELIVER,notEmpty" envDefault:"5"`

	// TenantEventTemporalCallTimeout bounds the one Temporal start the
	// handler makes per delivery, so a hung frontend cannot pin the
	// consumer's callback behind it. Management refuses to start unless it
	// is positive.
	TenantEventTemporalCallTimeout time.Duration `env:"PYCK_TENANT_EVENT_TEMPORAL_CALL_TIMEOUT,notEmpty" envDefault:"10s"`
}

type GraphQLConfig struct {
	GraphQLWebsocketKeepAliveInterval time.Duration `env:"PYCK_GRAPHQL_WEBSOCKET_KEEPALIVE_INTERVAL,notEmpty" envDefault:"20s"`
	GraphQLWebsocketPingPongInterval  time.Duration `env:"PYCK_GRAPHQL_WEBSOCKET_PINGPONG_INTERVAL,notEmpty" envDefault:"10s"`
	GraphQLQueryCacheSize             int           `env:"PYCK_GRAPHQL_QUERY_CACHE_SIZE,notEmpty" envDefault:"1000"`
	GraphQLComplexityLimit            int           `env:"PYCK_GRAPHQL_COMPLEXITY_LIMIT,notEmpty" envDefault:"1000"`
	GraphQLParserTokenLimit           int           `env:"PYCK_GRAPHQL_PARSER_TOKEN_LIMIT,notEmpty" envDefault:"1000000"`
}

type IdempotencyConfig struct {
	// IdempotencyMaxResponseBytes caps the serialized GraphQL response that
	// the Idempotency-Key feature will cache. A service with legitimately
	// larger responses can raise this without changing wire behavior for
	// other services; exceeding it rolls back and returns 413 rather than
	// degrading to a non-idempotent commit. Defaults to 1 MiB.
	IdempotencyMaxResponseBytes int `env:"PYCK_IDEMPOTENCY_MAX_RESPONSE_BYTES,notEmpty" envDefault:"1048576"`
}

// EventOutboxConfig holds the transactional-outbox tunables shared by every
// service (see common/events).
//
// Retries: a failed publish is retried with backoff 2^n seconds capped at 1h,
// so OutboxMaxRetries=15 spans about 4h before the transaction group is
// dead-lettered.
//
// Pruning: the OutboxJanitor deletes published rows (published_at set) older
// than OutboxPruneRetention, every OutboxPruneInterval, at most
// OutboxPruneBatchSize rows per statement, pausing OutboxPruneBatchPause between
// full batches to smooth WAL and replica lag. The 72h default equals the
// JetStream stream MaxAge. Unpublished and dead rows are never pruned.
type EventOutboxConfig struct {
	OutboxPollInterval         time.Duration `env:"PYCK_OUTBOX_POLL_INTERVAL,notEmpty" envDefault:"100ms"`
	OutboxBatchSize            int           `env:"PYCK_OUTBOX_BATCH_SIZE,notEmpty" envDefault:"100"`
	OutboxMaxRetries           int           `env:"PYCK_OUTBOX_MAX_RETRIES,notEmpty" envDefault:"15"`
	OutboxNotifyChannel        string        `env:"PYCK_OUTBOX_NOTIFY_CHANNEL,notEmpty" envDefault:"outbox_events"`
	OutboxListenerPingInterval time.Duration `env:"PYCK_OUTBOX_LISTENER_PING_INTERVAL,notEmpty" envDefault:"3s"`
	OutboxListenNotifyEnabled  bool          `env:"PYCK_OUTBOX_LISTEN_NOTIFY_ENABLED,notEmpty" envDefault:"true"`
	OutboxClaimLease           time.Duration `env:"PYCK_OUTBOX_CLAIM_LEASE,notEmpty" envDefault:"30s"`
	OutboxDLQDrainInterval     time.Duration `env:"PYCK_OUTBOX_DLQ_DRAIN_INTERVAL,notEmpty" envDefault:"5s"`
	OutboxPruneInterval        time.Duration `env:"PYCK_OUTBOX_PRUNE_INTERVAL,notEmpty" envDefault:"5m"`
	OutboxPruneRetention       time.Duration `env:"PYCK_OUTBOX_PRUNE_RETENTION,notEmpty" envDefault:"72h"`
	OutboxPruneBatchSize       int           `env:"PYCK_OUTBOX_PRUNE_BATCH_SIZE,notEmpty" envDefault:"1000"`
	OutboxPruneBatchPause      time.Duration `env:"PYCK_OUTBOX_PRUNE_BATCH_PAUSE,notEmpty" envDefault:"100ms"`
}
