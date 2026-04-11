// Package adapter provides different adapters for intercepting Temporal workflow events
package adapter

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/workflow"
	"github.com/pyck-ai/pyck/backend/temporal/config"
	"github.com/pyck-ai/pyck/backend/temporal/event"
	enumspb "go.temporal.io/api/enums/v1"
	temporalconfig "go.temporal.io/server/common/config"
	temporalpg "go.temporal.io/server/common/persistence/sql/sqlplugin/postgresql"
	"google.golang.org/grpc"
)

//go:embed postgres.sql.tmpl
var triggerFunctionSQL string

const (
	// TemporalDefaultAddr is the default address for Temporal server
	TemporalDefaultAddr = "localhost:7233"

	// Define the fixed offsets for the payload fields
	// These constants MUST match the configuration in the SQL trigger
	// --- Configuration (must match SQL) ---
	maxColumnChars  = 255
	maxBytesPerChar = 4
	maxRawBytes     = maxColumnChars * maxBytesPerChar // 1020

	// textLength is the exact slot size for the Base64-encoded string
	// This is the Go equivalent of: (ceil(MAX_RAW_BYTES / 3.0) * 4)::int
	// (1020 + 2) / 3 * 4 = 1022 / 3 * 4 = 340 * 4 = 1360
	textLength = ((maxRawBytes + 2) / 3) * 4 // 1360
	// ------------------------------------

	opStatusEnd   = 2
	uuidsEnd      = opStatusEnd + 44          // 46
	taskQueueEnd  = uuidsEnd + textLength     // 46 + 1360 = 1406
	typeNameEnd   = taskQueueEnd + textLength // 1406 + 1360 = 2766
	workflowIDEnd = typeNameEnd + textLength  // 2766 + 1360 = 4126
	payloadLength = workflowIDEnd
)

var (
	// ErrUnsupportedSQLPlugin indicates that the specified SQL plugin is not supported
	ErrUnsupportedSQLPlugin = errors.New("unsupported SQL plugin")
	// ErrCustomConnectNotSupported indicates that custom Connect function cannot be used
	ErrCustomConnectNotSupported = errors.New("PostgreSQL LISTEN adapter cannot use custom Connect function")
	// ErrDatabaseConnectionNil indicates that the database connection is nil
	ErrDatabaseConnectionNil = errors.New("database connection is nil - adapter not properly initialized")
	// ErrInvalidConfig indicates that the adapter configuration is invalid
	ErrInvalidConfig = errors.New("invalid adapter configuration")
	// ErrNamespaceNotFound is returned when a namespace ID is not found
	ErrNamespaceNotFound = errors.New("namespace ID not found")
	// ErrInvalidPayloadLength indicates that the payload length is incorrect
	ErrInvalidPayloadLength = errors.New("invalid payload length")
	// ErrInvalidOperationCode indicates that the operation code is invalid
	ErrInvalidOperationCode = errors.New("invalid operation code")
	// ErrInvalidUUIDLength indicates that the decoded UUID length is incorrect
	ErrInvalidUUIDLength = errors.New("invalid decoded UUID length")
	// ErrConnectionTimeout indicates that the connection attempt timed out
	ErrConnectionTimeout = errors.New("timed out waiting for PostgreSQL visibility database")
)

// PostgresAdapter uses PostgreSQL's LISTEN/NOTIFY to get real-time workflow events
// This requires creating a trigger on the executions_visibility table
type PostgresAdapter struct {
	DB          *sql.DB
	listener    *pq.Listener
	handler     *event.Handler
	ChannelName string
	Sqlconfig   *temporalconfig.SQL

	stopCh        chan struct{}
	wg            sync.WaitGroup
	clientFactory workflow.ClientFactory

	// connection retry configuration
	connectTimeout time.Duration
	retryInterval  time.Duration
}

// WorkflowEventPayload represents the JSON payload sent via NOTIFY
type WorkflowEventPayload struct {
	Operation        string                          `json:"op"`
	NamespaceID      string                          `json:"namespace_id"`
	TaskQueue        string                          `json:"task_queue"`
	WorkflowTypeName string                          `json:"workflow_type_name"`
	WorkflowID       string                          `json:"workflow_id"`
	RunID            string                          `json:"run_id"`
	Status           enumspb.WorkflowExecutionStatus `json:"status"`
}

// UnpackNotify is a pointer receiver method to populate the struct
// from the packed, fixed-length notification payload.
func (we *WorkflowEventPayload) UnpackNotify(payload string) error {
	// Check for exact length
	if len(payload) != payloadLength {
		return fmt.Errorf("%w: expected %d, got %d", ErrInvalidPayloadLength, payloadLength, len(payload))
	}

	// 1. Parse Op + Status (Bytes 0-1)
	opStatus := payload[0:opStatusEnd]
	switch opStatus[0] {
	case 'I':
		we.Operation = "INSERT"
	case 'U':
		we.Operation = "UPDATE"
	case 'D':
		we.Operation = "DELETE"
	default:
		return fmt.Errorf("%w: %c", ErrInvalidOperationCode, opStatus[0])
	}
	if opStatus[1] != '_' {
		we.Status = enumspb.WorkflowExecutionStatus(opStatus[1] - '0')
	} else {
		we.Status = 0
	}

	// 2. Parse Combined UUIDs (Bytes 2-45)
	uuidsBase64 := payload[opStatusEnd:uuidsEnd]
	uuidBytes, err := base64.StdEncoding.DecodeString(uuidsBase64)
	if err != nil {
		return fmt.Errorf("failed to base64 decode UUIDs: %w", err)
	}
	if len(uuidBytes) != 32 { // Expecting 2 UUIDs (32 bytes)
		return fmt.Errorf("%w: expected 32, got %d", ErrInvalidUUIDLength, len(uuidBytes))
	}

	// Split the 32 bytes into 2x 16-byte UUIDs and format them
	we.NamespaceID = formatUUID(uuidBytes[0:16])
	we.RunID = formatUUID(uuidBytes[16:32])

	// 3. Parse Base64-Encoded and Padded Text Fields (Bytes 46-end)
	taskQueueB64Padded := payload[uuidsEnd:taskQueueEnd]
	typeNameB64Padded := payload[taskQueueEnd:typeNameEnd]
	workflowIDB64Padded := payload[typeNameEnd:workflowIDEnd]

	// Helper function to trim padding and decode
	we.TaskQueue, err = decodePaddedBase64(taskQueueB64Padded)
	if err != nil {
		return fmt.Errorf("failed to decode task_queue: %w", err)
	}
	we.WorkflowTypeName, err = decodePaddedBase64(typeNameB64Padded)
	if err != nil {
		return fmt.Errorf("failed to decode workflow_type_name: %w", err)
	}
	we.WorkflowID, err = decodePaddedBase64(workflowIDB64Padded)
	if err != nil {
		return fmt.Errorf("failed to decode workflow_id: %w", err)
	}

	return nil
}

// decodePaddedBase64 trims the space padding, decodes the base64, and returns a string.
func decodePaddedBase64(paddedB64 string) (string, error) {
	// 1. Trim the padding
	trimmedB64 := strings.TrimRight(paddedB64, " ")
	if trimmedB64 == "" {
		return "", nil // It was an empty string
	}

	// 2. Decode
	decodedBytes, err := base64.StdEncoding.DecodeString(trimmedB64)
	if err != nil {
		return "", err
	}

	// 3. Convert to string
	return string(decodedBytes), nil
}

// formatUUID converts a 16-byte slice into a standard dashed UUID string.
func formatUUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	// Use hex.EncodeToString for high-performance hex conversion
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}

// Example main function to test the unpacking
// func main() {
// 	fmt.Println("Testing Go Payload Unpacker (Base64-Encoded Text)...")

// 	// --- Create a realistic test payload ---
// 	opStatus := "U7" // Update, Status 7

// 	// 2 UUIDs (as hex strings, no dashes)
// 	nsIDHex := "123e4567e89b12d3a456426614174000"
// 	runIDHex := "323e4567e89b12d3a456426614174002"

// 	// Convert hex to byte slices
// 	nsBytes, _ := hex.DecodeString(nsIDHex)
// 	runBytes, _ := hex.DecodeString(runIDHex)

// 	// Combine and base64 encode
// 	var combinedBytes []byte
// 	combinedBytes = append(combinedBytes, nsBytes...)
// 	combinedBytes = append(combinedBytes, runBytes...)
// 	uuidsBase64 := base64.StdEncoding.EncodeToString(combinedBytes) // 44 chars

// 	// Free text fields (Base64-encoded and padded)
// 	// NOTE: We must honor the maxColumnChars limit here for a realistic test
// 	taskQueue := "my-important-task-queue"
// 	typeName := "My.Workflow.Type::With::Colons"
// 	workflowID := "id-with-nulls-and-unicode: \000 \u2603" // The unsafe string

// 	// Helper to encode and pad
// 	padB64 := func(s string) string {
// 		// Truncate string to maxColumnChars (simulating SQL's LEFT())
// 		// This requires careful rune-to-string conversion
// 		if len(s) > maxColumnChars {
// 			s = string([]rune(s)[:maxColumnChars])
// 		}

// 		encoded := base64.StdEncoding.EncodeToString([]byte(s))
// 		return encoded + strings.Repeat(" ", textLength-len(encoded))
// 	}

// 	taskQueuePadded := padB64(taskQueue)
// 	typeNamePadded := padB64(typeName)
// 	workflowIDPadded := padB64(workflowID)

// 	// Final payload
// 	testPayload := opStatus + uuidsBase64 + taskQueuePadded + typeNamePadded + workflowIDPadded

// 	fmt.Printf("Test payload length: %d (Expected: %d)\n", len(testPayload), payloadLength)

// 	// --- Test the UnpackNotify method ---
// 	var event WorkflowEventPayload
// 	err := event.UnpackNotify(testPayload)

// 	if err != nil {
// 		fmt.Printf("Error unpacking: %v\n", err)
// 	} else {
// 		fmt.Println("\n--- Unpacked Data ---")
// 		fmt.Printf("Operation: %s\n", event.Operation)
// 		fmt.Printf("Status: %d\n", event.Status)
// 		fmt.Printf("NamespaceID: %s\n", event.NamespaceID)
// 		fmt.Printf("RunID: %s\n", event.RunID)
// 		fmt.Printf("TaskQueue: '%s'\n", event.TaskQueue)
// 		fmt.Printf("WorkflowTypeName: '%s'\n", event.WorkflowTypeName)
// 		// We print the hex representation to prove the unsafe bytes were transferred
// 		fmt.Printf("WorkflowID: '%s'\n", event.WorkflowID)
// 		fmt.Printf("WorkflowID (hex): %x\n", event.WorkflowID)
// 		fmt.Println("---------------------")
// 	}
// }

// NewPostgresAdapter creates a new PostgreSQL LISTEN adapter
func NewPostgresAdapter(
	handler *event.Handler,
	listenChannel string,
	temporalConfig *temporalconfig.Config,
) (*PostgresAdapter, error) {
	a := &PostgresAdapter{
		handler:     handler,
		ChannelName: listenChannel,
		stopCh:      make(chan struct{}),
		Sqlconfig:   temporalConfig.Persistence.DataStores[temporalConfig.Persistence.VisibilityStore].SQL,
		// sensible defaults; will be overridden from package config if available
		connectTimeout: 120 * time.Second,
		retryInterval:  1 * time.Second,
	}

	temporalAddr := TemporalDefaultAddr
	if v := os.Getenv("TEMPORAL_ADDRESS"); v != "" {
		temporalAddr = v
	}

	// connect to temporal api for workflowservice
	a.clientFactory = workflow.NewDefaultClientFactory(temporalAddr, nil)

	if a.ChannelName == "" {
		return nil, fmt.Errorf("%w: missing channel name", ErrInvalidConfig)
	}

	// override defaults from loaded environment config if present
	a.connectTimeout = config.Config.EventAdapterPostgresConnectTimeout
	a.retryInterval = config.Config.EventAdapterPostgresRetryInterval

	return a, nil
}

// buildDatabaseURL constructs the PostgreSQL connection string from config
func (a *PostgresAdapter) BuildDatabaseURL() (string, error) {
	switch a.Sqlconfig.PluginName {
	case temporalpg.PluginName, temporalpg.PluginNamePGX:
		// Supported
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedSQLPlugin, a.Sqlconfig.PluginName)
	}

	if a.Sqlconfig.Connect != nil {
		return "", ErrCustomConnectNotSupported
	}

	var databaseURL string

	if a.Sqlconfig.User != "" && a.Sqlconfig.Password != "" {
		databaseURL = fmt.Sprintf("postgres://%s:%s@%s/%s",
			a.Sqlconfig.User,
			a.Sqlconfig.Password,
			a.Sqlconfig.ConnectAddr,
			a.Sqlconfig.DatabaseName,
		)
	} else {
		databaseURL = fmt.Sprintf("postgres://%s/%s",
			a.Sqlconfig.ConnectAddr,
			a.Sqlconfig.DatabaseName,
		)
	}

	if a.Sqlconfig.TLS != nil && a.Sqlconfig.TLS.Enabled {
		if a.Sqlconfig.TLS.ServerName != "" {
			databaseURL += fmt.Sprintf("?sslmode=require&sslrootcert=%s", a.Sqlconfig.TLS.CaFile)
		} else {
			databaseURL += "?sslmode=require"
		}
	} else {
		databaseURL += "?sslmode=disable"
	}

	return databaseURL, nil
}

func (a *PostgresAdapter) connect(timeout time.Duration, reconnectInterval time.Duration) (*pq.Listener, error) {
	databaseURL, err := a.BuildDatabaseURL()
	if err != nil {
		return nil, err
	}

	return pq.NewListener(
		databaseURL,
		timeout,
		reconnectInterval,
		a.listenerError,
	), nil
}

func (a *PostgresAdapter) listenerError(ev pq.ListenerEventType, err error) {
	if err != nil {
		log.ForContext(context.Background()).Error().
			Err(err).
			Msg("PostgreSQL listener error")
	}
}

// SetupTrigger creates the necessary trigger and function in PostgreSQL
// This only needs to be run once during setup
func (a *PostgresAdapter) SetupTrigger(ctx context.Context) error {
	logger := log.ForContext(ctx)

	// Ensure database connection is available
	if a.DB == nil {
		return ErrDatabaseConnectionNil
	}

	// Parse the embedded SQL template
	tmpl, err := template.New("trigger_function").Parse(triggerFunctionSQL)
	if err != nil {
		return fmt.Errorf("failed to parse trigger function template: %w", err)
	}

	// Execute the template with channel name
	var sqlBuf strings.Builder
	templateData := struct {
		ChannelName string
	}{
		ChannelName: a.ChannelName,
	}

	if err := tmpl.Execute(&sqlBuf, templateData); err != nil {
		return fmt.Errorf("failed to execute trigger function template: %w", err)
	}

	if _, err := a.DB.ExecContext(ctx, sqlBuf.String()); err != nil {
		return fmt.Errorf("failed to create trigger: %w", err)
	}

	logger.Info().
		Str("channel", a.ChannelName).
		Msg("PostgreSQL trigger and function created successfully")

	return nil
}

// Start begins listening for PostgreSQL notifications.
// This method will retry connecting to the database for a configurable amount
// of time before giving up. It is intended to be called after the Temporal
// server has been started so that the underlying visibility database exists.
func (a *PostgresAdapter) Start(ctx context.Context) error {
	logger := log.ForContext(ctx)

	deadline := time.Now().Add(a.connectTimeout)

	for {
		// respect cancellation
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w after %s", ErrConnectionTimeout, a.connectTimeout)
		}

		// Build database URL
		databaseURL, err := a.BuildDatabaseURL()
		if err != nil {
			logger.Error().Err(err).Msg("failed to build database URL")
			time.Sleep(a.retryInterval)
			continue
		}

		// Try to open DB
		db, err := sql.Open("postgres", databaseURL)
		if err != nil {
			logger.Debug().Err(err).Msg("failed to open database, will retry")
			time.Sleep(a.retryInterval)
			continue
		}

		// Test database connectivity with a short timeout
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		pingErr := db.PingContext(pingCtx)
		cancel()
		if pingErr != nil {
			logger.Info().Err(pingErr).Msg("database not ready yet, retrying")
			db.Close()
			time.Sleep(a.retryInterval)
			continue
		}

		// Persist successful DB connection
		a.DB = db

		// Create listener (use retryInterval as reconnectInterval)
		a.listener, err = a.connect(90*time.Second, a.retryInterval)
		if err != nil {
			logger.Error().Err(err).Msg("failed to create PostgreSQL listener, retrying")
			a.DB.Close()
			time.Sleep(a.retryInterval)
			continue
		}

		// Ensure trigger exists (may fail while DB not fully ready)
		if err := a.SetupTrigger(ctx); err != nil {
			logger.Error().Err(err).Msg("failed to setup trigger, will retry")
			_ = a.listener.Close()
			_ = a.DB.Close()
			time.Sleep(a.retryInterval)
			continue
		}

		// Start listening on the channel
		if err := a.listener.Listen(a.ChannelName); err != nil {
			logger.Error().Err(err).Str("channel", a.ChannelName).Msg("failed to listen on channel, will retry")
			_ = a.listener.Close()
			_ = a.DB.Close()
			time.Sleep(a.retryInterval)
			continue
		}

		logger.Info().
			Str("channel", a.ChannelName).
			Msg("started PostgreSQL LISTEN adapter")

		a.wg.Add(1)
		go a.listenLoop(ctx)

		return nil
	}
}

// Stop stops the PostgreSQL listener
func (a *PostgresAdapter) Stop() error {
	// Close stop channel if it exists
	if a.stopCh != nil {
		close(a.stopCh)
	}
	a.wg.Wait()

	// Close listener if it exists
	if a.listener != nil {
		if err := a.listener.Close(); err != nil {
			return fmt.Errorf("failed to close listener: %w", err)
		}
	}

	// Close database connection if it exists
	if a.DB != nil {
		if err := a.DB.Close(); err != nil {
			return fmt.Errorf("failed to close database: %w", err)
		}
	}

	return nil
}

// GetInterceptor returns nil as this adapter doesn't use interceptors
func (a *PostgresAdapter) GetInterceptor() grpc.UnaryServerInterceptor {
	return nil
}

// listenLoop processes PostgreSQL notifications
func (a *PostgresAdapter) listenLoop(ctx context.Context) {
	defer a.wg.Done()

	logger := log.ForContext(ctx)

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("PostgreSQL listener stopped: context cancelled")
			return

		case <-a.stopCh:
			logger.Info().Msg("PostgreSQL listener stopped")
			return

		case notification := <-a.listener.Notify:
			if notification == nil {
				// Notification channel closed, listener died
				logger.Error().Msg("listener notification channel closed")
				// Could implement reconnection logic here
				continue
			}

			// Process the notification
			a.wg.Add(1)
			go func() {
				defer a.wg.Done()
				a.processNotification(ctx, notification)
			}()

		case <-time.After(90 * time.Second):
			// Send periodic ping to keep connection alive
			go func() {
				if err := a.listener.Ping(); err != nil {
					logger.Error().Err(err).Msg("listener ping failed")
				}
			}()
		}
	}
}

// resolveNamespace resolves a namespace ID to its name
func (a *PostgresAdapter) resolveNamespace(ctx context.Context, namespaceID string) (string, error) {
	client, err := a.clientFactory.GetClient(ctx, "")
	if err != nil {
		return "", fmt.Errorf("failed to get Temporal client: %w", err)
	}

	namespace, err := client.GetNamespaceByID(ctx, namespaceID)
	if err != nil {
		return "", fmt.Errorf("failed to get namespace by ID: %w", err)
	}

	return namespace.GetName(), nil
}

// processNotification processes a single PostgreSQL notification
func (a *PostgresAdapter) processNotification(ctx context.Context, notification *pq.Notification) {
	logger := log.ForContext(ctx)

	// Parse the JSON payload
	var payload WorkflowEventPayload
	if err := payload.UnpackNotify(notification.Extra); err != nil {
		logger.Error().
			Err(err).
			Bytes("payload", []byte(notification.Extra)).
			Msg("failed to unpack notification payload")
		return
	}

	logger.Debug().
		Str("operation", payload.Operation).
		Str("namespace_id", payload.NamespaceID).
		Str("task_queue", payload.TaskQueue).
		Str("workflow_type_name", payload.WorkflowTypeName).
		Str("workflow_id", payload.WorkflowID).
		Str("run_id", payload.RunID).
		Str("status", payload.Status.String()).
		Msg("received workflow event notification")

	// Skip DELETE operations (workflow cleanup)
	if payload.Operation == "DELETE" {
		return
	}

	// Resolve namespace ID to namespace name
	namespace, err := a.resolveNamespace(ctx, payload.NamespaceID)
	if err != nil {
		logger.Error().
			Err(err).
			Str("namespace_id", payload.NamespaceID).
			Msg("failed to resolve namespace ID to name")
		return
	}

	// Send to handler
	a.handler.Notify(ctx, &events.TemporalWorkflowStateChangeMessage{
		Namespace:        namespace, // Now using actual namespace name
		TaskQueue:        payload.TaskQueue,
		WorkflowTypeName: payload.WorkflowTypeName,
		WorkflowID:       payload.WorkflowID,
		RunID:            payload.RunID,
		Status:           payload.Status.String(),
	})
}

// RemoveTrigger removes the trigger and function from PostgreSQL
// This should be called during cleanup or uninstall
func (a *PostgresAdapter) RemoveTrigger(ctx context.Context) error {
	// Ensure database connection is available
	if a.DB == nil {
		return ErrDatabaseConnectionNil
	}

	// Remove trigger
	if _, err := a.DB.ExecContext(ctx, "DROP TRIGGER IF EXISTS workflow_change_trigger ON executions_visibility"); err != nil {
		return fmt.Errorf("failed to drop trigger: %w", err)
	}

	// Remove function
	if _, err := a.DB.ExecContext(ctx, "DROP FUNCTION IF EXISTS notify_workflow_change()"); err != nil {
		return fmt.Errorf("failed to drop function: %w", err)
	}

	return nil
}

// GetMetrics returns adapter metrics
func (a *PostgresAdapter) GetMetrics() map[string]interface{} {
	return map[string]interface{}{
		"type":    "postgres_listen",
		"channel": a.ChannelName,
		// Could add connection status, notifications received, etc.
	}
}
