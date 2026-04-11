package util

import (
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// TestContext holds common test variables and setup
type TestContext struct {
	ValidToken string
	NatsConn   *nats.Conn
	JetStream  jetstream.JetStream
	TenantID   string
}

// NewTestContext creates a new test context with the given token
func NewTestContext(token, tenantID string) (*TestContext, error) {
	var err error
	ctx := &TestContext{
		ValidToken: token,
	}

	if len(tenantID) == 0 {
		// Extract tenant ID
		tenantID, err = GetTenantIDFromJWT(token)
		if err != nil {
			return nil, err
		}
	}

	ctx.TenantID = tenantID

	return ctx, nil
}

// Setup establishes NATS connection and JetStream
func (tc *TestContext) Setup() error {
	// Create NATS connection
	conn, err := CreateTestConnection(tc.ValidToken)
	if err != nil {
		return err
	}
	tc.NatsConn = conn

	// Create JetStream context if connection successful
	if conn != nil {
		js, err := jetstream.New(conn)
		if err != nil {
			conn.Close()
			return err
		}
		tc.JetStream = js
	}

	return nil
}

// Cleanup closes connections
func (tc *TestContext) Cleanup() {
	if tc.NatsConn != nil {
		tc.NatsConn.Close()
		tc.NatsConn = nil
		tc.JetStream = nil
	}
}
