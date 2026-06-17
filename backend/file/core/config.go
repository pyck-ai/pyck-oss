package core

import (
	"context"

	"github.com/pyck-ai/pyck/backend/common/env"
	envconfig "github.com/pyck-ai/pyck/backend/common/env/config"
	"github.com/pyck-ai/pyck/backend/common/otel"
)

type config struct {
	envconfig.DbConfig
	envconfig.EnvironmentConfig
	envconfig.IdempotencyConfig
	envconfig.EventOutboxConfig
	envconfig.GatewayConfig
	envconfig.GraphQLConfig
	envconfig.HTTPConfig
	envconfig.LogConfig
	envconfig.NatsConfig
	envconfig.ServiceConfig
	envconfig.ServiceInstanceConfig
	envconfig.TemporalConfig
	envconfig.ZitadelConfig

	otel.OTelConfig

	AwsAccessKeyId       string `env:"PYCK_AWS_ACCESS_KEY_ID"`
	AwsS3Bucket          string `env:"PYCK_AWS_S3_BUCKET"`
	AwsS3EndpointUrl     string `env:"PYCK_AWS_S3_ENDPOINT_URL"`
	AwsS3HttpEndpointUrl string `env:"PYCK_AWS_S3_HTTP_ENDPOINT_URL"`
	AwsS3Region          string `env:"PYCK_AWS_S3_REGION"`
	AwsSecretAccessKey   string `env:"PYCK_AWS_SECRET_ACCESS_KEY" json:"-"`

	OpenAiToken string `env:"PYCK_OPENAI_TOKEN" envDefault:"" json:"-"`
}

var Config config

func LoadEnv() (err error) {
	_, Config, err = env.Load[config](context.TODO())
	return err
}
