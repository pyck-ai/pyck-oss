module github.com/pyck-ai/pyck/backend/cli

go 1.25.0

tool (
	github.com/dmarkham/enumer
	github.com/pyck-ai/pyck/backend/common/cmd/entc
)

replace (
	github.com/pyck-ai/pyck/backend/common => ../common
	github.com/pyck-ai/pyck/backend/inventory => ../inventory
	github.com/pyck-ai/pyck/backend/main-data => ../main-data
	github.com/pyck-ai/pyck/backend/management => ../management
	github.com/pyck-ai/pyck/backend/picking => ../picking
	github.com/pyck-ai/pyck/backend/receiving => ../receiving
	github.com/pyck-ai/pyck/backend/workflow => ../workflow
)

require (
	github.com/Yamashou/gqlgenc v0.33.0
	github.com/brianvoe/gofakeit/v6 v6.28.0
	github.com/google/uuid v1.6.0
	github.com/pyck-ai/pyck/backend/common v0.0.0-00010101000000-000000000000
	github.com/pyck-ai/pyck/backend/inventory v0.0.0-00010101000000-000000000000
	github.com/pyck-ai/pyck/backend/main-data v0.0.0-00010101000000-000000000000
	github.com/pyck-ai/pyck/backend/management v0.0.0-00010101000000-000000000000
	github.com/pyck-ai/pyck/backend/picking v0.0.0-00010101000000-000000000000
	github.com/pyck-ai/pyck/backend/receiving v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.10.2
	github.com/spf13/viper v1.21.0
	golang.org/x/tools v0.42.0
)

require (
	ariga.io/atlas v0.38.0 // indirect
	entgo.io/contrib v0.7.0 // indirect
	entgo.io/ent v0.14.5 // indirect
	github.com/99designs/gqlgen v0.17.88 // indirect
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/bmatcuk/doublestar v1.3.4 // indirect
	github.com/caarlos0/env/v11 v11.3.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.7 // indirect
	github.com/dmarkham/enumer v1.6.3 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-chi/chi/v5 v5.2.5 // indirect
	github.com/go-chi/httplog v0.3.2 // indirect
	github.com/go-jose/go-jose/v4 v4.1.3 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/inflect v0.21.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/gorilla/securecookie v1.1.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.4 // indirect
	github.com/hashicorp/errwrap v1.0.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/hcl/v2 v2.24.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/muhlemmer/gu v0.3.1 // indirect
	github.com/pascaldekloe/name v1.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/riandyrn/otelchi v0.12.2 // indirect
	github.com/rs/zerolog v1.34.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/sosodev/duration v1.4.0 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/urfave/cli/v2 v2.27.7 // indirect
	github.com/vektah/gqlparser/v2 v2.5.32 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/xrash/smetrics v0.0.0-20240521201337-686a1a2994c1 // indirect
	github.com/zclconf/go-cty v1.16.3 // indirect
	github.com/zclconf/go-cty-yaml v1.1.0 // indirect
	github.com/zitadel/logging v0.7.0 // indirect
	github.com/zitadel/oidc/v3 v3.45.5 // indirect
	github.com/zitadel/schema v1.3.2 // indirect
	github.com/zitadel/zitadel-go/v3 v3.26.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.61.0 // indirect
	go.opentelemetry.io/otel v1.40.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.38.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.38.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.38.0 // indirect
	go.opentelemetry.io/otel/metric v1.40.0 // indirect
	go.opentelemetry.io/otel/sdk v1.38.0 // indirect
	go.opentelemetry.io/otel/trace v1.40.0 // indirect
	go.opentelemetry.io/proto/otlp v1.7.1 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546 // indirect
	golang.org/x/mod v0.33.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20251222181119-0a764e51fe1b // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251222181119-0a764e51fe1b // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
