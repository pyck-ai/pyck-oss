package config

import (
	"time"

	"github.com/pyck-ai/pyck/backend/common/typing"
)

type HTTPConfig struct {
	HTTPHost              string        `env:"PYCK_HTTP_HOST" envDefault:""`
	HTTPPort              int           `env:"PYCK_HTTP_PORT,notEmpty" envDefault:"8080"`
	HTTPReadHeaderTimeout time.Duration `env:"PYCK_HTTP_READ_HEADER_TIMEOUT,notEmpty" envDefault:"5s"`
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
}

type TemporalConfig struct {
	TemporalUrl string `env:"PYCK_TEMPORAL_URL,notEmpty,required"`
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
}

type NatsConfig struct {
	NatsUrl            string        `env:"PYCK_NATS_URL,notEmpty,required" json:"-"`
	NatsStreamName     string        `env:"PYCK_NATS_STREAM_NAME,notEmpty,required"`
	NatsWsUrl          string        `env:"PYCK_NATS_WS_URL,notEmpty,required"`
	NatsReplicasNumber int           `env:"PYCK_NATS_REPLICAS_NO,notEmpty,required"`
	NatsReplyTimeout   time.Duration `env:"PYCK_NATS_REPLY_TIMEOUT,notEmpty" envDefault:"100ms"`
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

type EventOutboxConfig struct {
	OutboxPollInterval         time.Duration `env:"PYCK_OUTBOX_POLL_INTERVAL,notEmpty" envDefault:"100ms"`
	OutboxBatchSize            int           `env:"PYCK_OUTBOX_BATCH_SIZE,notEmpty" envDefault:"100"`
	OutboxReplyTimeout         time.Duration `env:"PYCK_OUTBOX_REPLY_TIMEOUT,notEmpty" envDefault:"5s"`
	OutboxMaxRetries           int           `env:"PYCK_OUTBOX_MAX_RETRIES,notEmpty" envDefault:"10"`
	OutboxNotifyChannel        string        `env:"PYCK_OUTBOX_NOTIFY_CHANNEL,notEmpty" envDefault:"outbox_events"`
	OutboxListenerPingInterval time.Duration `env:"PYCK_OUTBOX_LISTENER_PING_INTERVAL,notEmpty" envDefault:"3s"`
	OutboxReplyCleanupInterval time.Duration `env:"PYCK_OUTBOX_REPLY_CLEANUP_INTERVAL,notEmpty" envDefault:"5s"`
	OutboxListenNotifyEnabled  bool          `env:"PYCK_OUTBOX_LISTEN_NOTIFY_ENABLED,notEmpty" envDefault:"true"`
	OutboxClaimLease           time.Duration `env:"PYCK_OUTBOX_CLAIM_LEASE,notEmpty" envDefault:"30s"`
	OutboxDLQDrainInterval     time.Duration `env:"PYCK_OUTBOX_DLQ_DRAIN_INTERVAL,notEmpty" envDefault:"5s"`
}
