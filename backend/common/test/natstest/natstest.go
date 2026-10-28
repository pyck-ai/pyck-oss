// Package natstest runs an embedded NATS server with JetStream for tests.
//
// The accounts mirror config/nats/nats.conf. Every user logs in with its name
// as password. ServiceUser has no restrictions and is in the JetStream-enabled
// account, like the services' auth_users credentials. Start adds users
// restricted to the given permissions to the tenant account, which reaches
// JetStream only through its imports, like the clients the auth callout
// admits.
package natstest

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

const (
	// ServiceUser is the unrestricted user of the server.
	ServiceUser = "service"

	// StreamAccount is the services' account of config/nats/nats.conf,
	// which holds the streams.
	StreamAccount = "pyck"
	// TenantAccount is the account the auth callout places tenant clients
	// in.
	TenantAccount = "pyck-tenants"
)

// TenantExports are the stream account's exports to the tenant account, as
// config/nats/nats.conf declares them; the tenant account imports each.
//
//nolint:gochecknoglobals // read-only table
var TenantExports = []Export{
	{Subject: "$JS.API.INFO"},
	{Subject: "$JS.API.STREAM.INFO.*"},
	{Subject: "$JS.API.CONSUMER.CREATE.>"},
	{Subject: "$JS.API.CONSUMER.INFO.>"},
	{Subject: "$JS.API.CONSUMER.DELETE.>"},
	{Subject: "$JS.API.CONSUMER.MSG.NEXT.>", StreamResponse: true, ResponseThreshold: time.Minute},
	{Subject: "$JS.ACK.>"},
	{Subject: "pyck.*.>"},
}

// Export is a service export of the stream account to the tenant account.
type Export struct {
	Subject string
	// StreamResponse marks a service answered with several messages
	// (response_type: stream).
	StreamResponse bool
	// ResponseThreshold bounds how long the server routes answers to one
	// request; zero is the server's default.
	ResponseThreshold time.Duration
}

type (
	subjectPermission struct {
		Allow []string `json:"allow,omitempty"`
		Deny  []string `json:"deny,omitempty"`
	}

	permissions struct {
		Publish   subjectPermission `json:"publish"`
		Subscribe subjectPermission `json:"subscribe"`
	}

	user struct {
		User        string       `json:"user"`
		Password    string       `json:"password"`
		Permissions *permissions `json:"permissions,omitempty"`
	}
)

// Start runs a server for the duration of tb. users maps the name of each
// restricted user to its permissions; settings override top-level server
// settings, such as "max_payload".
func Start(tb testing.TB, users map[string]jwt.Permissions, settings ...map[string]any) *server.Server {
	tb.Helper()

	tenantUsers := make([]user, 0, len(users))
	for name, perms := range users {
		tenantUsers = append(tenantUsers, user{
			User:     name,
			Password: name,
			Permissions: &permissions{
				Publish:   subjectPermission{Allow: perms.Pub.Allow, Deny: perms.Pub.Deny},
				Subscribe: subjectPermission{Allow: perms.Sub.Allow, Deny: perms.Sub.Deny},
			},
		})
	}

	exports := make([]map[string]any, 0, len(TenantExports))
	imports := make([]map[string]any, 0, len(TenantExports))
	for _, export := range TenantExports {
		entry := map[string]any{"service": export.Subject, "accounts": []string{TenantAccount}}
		if export.StreamResponse {
			entry["response_type"] = "stream"
		}
		if export.ResponseThreshold > 0 {
			entry["response_threshold"] = export.ResponseThreshold.String()
		}
		exports = append(exports, entry)
		imports = append(imports, map[string]any{
			"service": map[string]any{"account": StreamAccount, "subject": export.Subject},
		})
	}

	dir := tb.TempDir()

	// The configuration format is a superset of JSON, without its \u escapes,
	// so the wildcard ">" must not be escaped as HTML.
	var encoded bytes.Buffer

	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	conf := map[string]any{
		"host":      "127.0.0.1",
		"port":      -1,
		"jetstream": map[string]any{"store_dir": filepath.Join(dir, "jetstream")},
		"accounts": map[string]any{
			StreamAccount: map[string]any{
				"jetstream": "enabled",
				"users":     []user{{User: ServiceUser, Password: ServiceUser}},
				"exports":   exports,
			},
			TenantAccount: map[string]any{
				"users":   tenantUsers,
				"imports": imports,
			},
		},
	}
	for _, overrides := range settings {
		maps.Copy(conf, overrides)
	}

	require.NoError(tb, encoder.Encode(conf))

	confPath := filepath.Join(dir, "nats.conf")
	require.NoError(tb, os.WriteFile(confPath, encoded.Bytes(), 0o600))

	opts, err := server.ProcessConfigFile(confPath)
	require.NoError(tb, err)

	opts.NoLog = true
	opts.NoSigs = true

	srv, err := server.NewServer(opts)
	require.NoError(tb, err)

	go srv.Start()

	require.True(tb, srv.ReadyForConnections(10*time.Second), "embedded NATS did not start")
	tb.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})

	return srv
}

// Connect connects name to srv for the duration of tb, with opts such as an
// inbox prefix. Permission violations are reported on errs, when it is not
// nil, instead of being dropped.
func Connect(tb testing.TB, srv *server.Server, name string, errs chan<- error, opts ...nats.Option) *nats.Conn {
	tb.Helper()

	opts = append(opts, nats.UserInfo(name, name))
	if errs != nil {
		opts = append(opts, nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case errs <- err:
			default:
			}
		}))
	}

	nc, err := nats.Connect(srv.ClientURL(), opts...)
	require.NoError(tb, err)
	tb.Cleanup(nc.Close)

	return nc
}

// StoredOn returns how many messages stream holds on the subjects filter
// matches. It reports no test failure itself, so polling helpers can call it
// from goroutines that outlive the test.
func StoredOn(ctx context.Context, js jetstream.JetStream, stream, filter string) (uint64, error) {
	handle, err := js.Stream(ctx, stream)
	if err != nil {
		return 0, err
	}

	info, err := handle.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return 0, err
	}

	var count uint64
	for _, n := range info.State.Subjects {
		count += n
	}

	return count, nil
}

// SerialLock runs one fn at a time, as the services' advisory lock does
// across processes: an events.StreamLock for the topologies of one test.
func SerialLock() func(ctx context.Context, fn func(context.Context) error) error {
	var mu sync.Mutex

	return func(ctx context.Context, fn func(context.Context) error) error {
		mu.Lock()
		defer mu.Unlock()

		return fn(ctx)
	}
}
