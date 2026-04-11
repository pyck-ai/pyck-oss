package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/iancoleman/strcase"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog/log"
)

// Define static errors to avoid dynamic error creation
var (
	ErrAllEventsMarshalFailed = errors.New("all events failed to marshal")
	ErrQuickwitBadStatus      = errors.New("quickwit returned bad status")
	ErrIndexCreationFailed    = errors.New("failed to create index")
)

// EventWithMessage holds an event and its corresponding NATS message
type EventWithMessage struct {
	Event   any
	Message jetstream.Msg
}

type QuickwitSyncService struct {
	quickwitURL  string
	batchSize    int
	batchTimeout time.Duration
	eventBatch   map[uuid.UUID][]EventWithMessage
	batchMutex   sync.Mutex
	ticker       *time.Ticker
	httpClient   *http.Client
	shutdownCh   chan struct{}
}

func NewQuickwitSyncService(quickwitURL string, batchSize int, batchTimeout time.Duration) *QuickwitSyncService {
	return &QuickwitSyncService{
		quickwitURL:  quickwitURL,
		batchSize:    batchSize,
		batchTimeout: batchTimeout,
		eventBatch:   make(map[uuid.UUID][]EventWithMessage),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (s *QuickwitSyncService) ListenToEvents(consumer jetstream.Consumer) {
	s.startBatchTimer()

	cons, err := consumer.Messages()
	if err != nil {
		log.Error().Err(err).Msg("Failed to create consumer messages")
		return
	}

	for {
		select {
		case <-s.shutdownCh:
			log.Info().Msg("QuickwitSyncService shutting down")
			return
		default:
			msg, err := cons.Next()
			if err != nil {
				log.Error().Err(err).Msg("Error getting next message")
				continue
			}

			s.processMessage(msg)
		}
	}
}

func (s *QuickwitSyncService) processMessage(msg jetstream.Msg) {
	metadata, _ := msg.Metadata()
	subject := msg.Subject()

	tenantID := extractTenantID(subject)
	if tenantID == nil {
		systemID := uuid.Max
		tenantID = &systemID
	} else {
		// Validate the UUID is not malformed
		if *tenantID == uuid.Nil {
			log.Warn().Str("subject", subject).Msg("Invalid tenant ID in subject - using system tenant")
			systemID := uuid.Max
			tenantID = &systemID
		}
	}

	event := s.parseEvent(msg, subject, metadata)
	if event == nil {
		// ACK even if we can't parse, to avoid redelivery of bad messages
		if err := msg.Ack(); err != nil {
			log.Error().Err(err).Msg("Failed to ACK unparseable message")
		}
		return
	}

	s.batchMutex.Lock()
	s.eventBatch[*tenantID] = append(s.eventBatch[*tenantID], EventWithMessage{
		Event:   event,
		Message: msg,
	})

	if len(s.eventBatch[*tenantID]) >= s.batchSize {
		// Copy batch and remove from map while holding lock
		batch := make([]EventWithMessage, len(s.eventBatch[*tenantID]))
		copy(batch, s.eventBatch[*tenantID])
		delete(s.eventBatch, *tenantID)
		s.batchMutex.Unlock()

		// Process batch outside mutex
		s.processBatch(*tenantID, batch)
	} else {
		s.batchMutex.Unlock()
		// Note: Message is NOT ACKed here - it will be ACKed when the batch is processed
	}
}

func (s *QuickwitSyncService) parseEvent(msg jetstream.Msg, subject string, metadata *jetstream.MsgMetadata) any {
	// Create base event with common fields
	event := map[string]any{
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"subject":     subject,
		"sequence":    metadata.Sequence.Consumer,
		"stream_name": metadata.Stream,
	}

	// Parse subject to determine event type
	parts := strings.Split(subject, ".")
	if len(parts) < 3 {
		return nil
	}

	// Unmarshal message data as generic map
	var msgData map[string]any
	if err := json.Unmarshal(msg.Data(), &msgData); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal message data")
		// Store raw data if unmarshal fails
		event["event_type"] = "unknown"
		event["raw_data"] = string(msg.Data())
		return event
	}

	// Merge message data into event
	for k, v := range msgData {
		// Convert field names to snake_case
		snakeKey := strcase.ToSnake(k)

		// Special handling for specific fields
		switch k {
		case "ID":
			// Map ID to entity_id
			event["entity_id"] = v
		case "DataID":
			// Map DataID to entity_id
			event["entity_id"] = v
		case "Type":
			// Map Type to custom_type for custom events
			event["custom_type"] = v
		case "WfAssignee":
			// Map WfAssignee to workflow_assignee
			if assignee, ok := v.(string); ok && assignee != "" {
				event["workflow_assignee"] = v
			}
		case "WfSearchAttributes":
			// Map WfSearchAttributes to workflow_search_attributes
			if attrs, ok := v.(map[string]any); ok && len(attrs) > 0 {
				event["workflow_search_attributes"] = v
			}
		default:
			// Use the snake_case version of the field name
			event[snakeKey] = v
		}
	}

	// Add full-text searchable version of all message data
	event["data_fulltext"] = flattenForFullText(msgData)

	// Determine event type from subject
	if len(parts) > 2 && parts[2] == "crud" {
		event["event_type"] = "mutation"
	} else if strings.Contains(subject, ".custom-events") {
		event["event_type"] = "custom"
	} else if strings.Contains(subject, ".workflows.") {
		event["event_type"] = "workflow"
	} else if strings.Contains(subject, ".update") {
		event["event_type"] = "update"
	} else {
		event["event_type"] = "unknown"
	}

	return event
}

// flattenForFullText recursively flattens a map to a space-separated string
func flattenForFullText(data any) string {
	if data == nil {
		return ""
	}

	const maxSize = 10000 // 10KB limit
	const maxDepth = 10

	var builder strings.Builder
	builder.Grow(1024) // Pre-allocate some space for efficiency

	flattenRecursive(data, &builder, 0, maxDepth, maxSize)
	return builder.String()
}

func flattenRecursive(data any, builder *strings.Builder, depth, maxDepth, maxSize int) {
	// Safety checks for depth and size only
	if depth >= maxDepth || builder.Len() >= maxSize {
		return
	}

	// Add space before each value (except the first)
	if builder.Len() > 0 {
		builder.WriteByte(' ')
	}

	switch v := data.(type) {
	case nil:
		// Skip nil values
		return
	case map[string]any:
		for key, val := range v {
			if builder.Len() >= maxSize {
				return
			}
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteString(key)
			flattenRecursive(val, builder, depth+1, maxDepth, maxSize)
		}
	case []any:
		for _, item := range v {
			if builder.Len() >= maxSize {
				return
			}
			flattenRecursive(item, builder, depth+1, maxDepth, maxSize)
		}
	case string:
		if v != "" {
			builder.WriteString(v)
		}
	case float64:
		fmt.Fprintf(builder, "%g", v)
	case float32:
		fmt.Fprintf(builder, "%g", v)
	case int:
		builder.WriteString(strconv.Itoa(v))
	case int64:
		builder.WriteString(strconv.FormatInt(v, 10))
	case int32:
		builder.WriteString(strconv.FormatInt(int64(v), 10))
	case uint:
		builder.WriteString(strconv.FormatUint(uint64(v), 10))
	case uint64:
		builder.WriteString(strconv.FormatUint(v, 10))
	case uint32:
		builder.WriteString(strconv.FormatUint(uint64(v), 10))
	case bool:
		builder.WriteString(strconv.FormatBool(v))
	default:
		// Handle any other types by converting to string
		if s := fmt.Sprintf("%v", v); s != "" && s != "<nil>" {
			builder.WriteString(s)
		}
	}
}

func (s *QuickwitSyncService) startBatchTimer() {
	s.shutdownCh = make(chan struct{})
	s.ticker = time.NewTicker(s.batchTimeout)
	go func() {
		for {
			select {
			case <-s.ticker.C:
				s.flushAllBatches()
			case <-s.shutdownCh:
				return
			}
		}
	}()
}

func (s *QuickwitSyncService) flushAllBatches() {
	s.batchMutex.Lock()
	if len(s.eventBatch) == 0 {
		s.batchMutex.Unlock()
		return
	}

	// Atomic swap of the entire map
	batches := s.eventBatch
	s.eventBatch = make(map[uuid.UUID][]EventWithMessage)
	s.batchMutex.Unlock()

	for tenantID, batch := range batches {
		if len(batch) > 0 {
			s.processBatch(tenantID, batch)
		}
	}
}

// processBatch handles ingestion and ACK/NAK for a batch of events
func (s *QuickwitSyncService) processBatch(tenantID uuid.UUID, batch []EventWithMessage) {
	// Create logger with tenant context
	logger := log.With().Str("tenant_id", tenantID.String()).Logger()

	// Extract just the events for ingestion
	events := make([]any, len(batch))
	for i, item := range batch {
		events[i] = item.Event
	}

	// Try to ingest the batch
	if err := s.ingestEventsForTenant(tenantID, events); err != nil {
		logger.Error().Err(err).Int("batch_size", len(batch)).Msg("Failed to ingest batch, NAKing all messages")

		// NAK all messages in the batch for retry
		for _, item := range batch {
			if nakErr := item.Message.Nak(); nakErr != nil {
				logger.Error().Err(nakErr).Msg("Failed to NAK message")
			}
		}
		return
	}

	// Success - ACK all messages in the batch
	for _, item := range batch {
		if ackErr := item.Message.Ack(); ackErr != nil {
			logger.Error().Err(ackErr).Msg("Failed to ACK message after successful ingestion")
		}
	}

	logger.Debug().Int("batch_size", len(batch)).Msg("Successfully processed batch")
}

func (s *QuickwitSyncService) ingestEventsForTenant(tenantID uuid.UUID, events []any) error {
	indexName := fmt.Sprintf("pyck-events-%s", tenantID)
	if tenantID == uuid.Max {
		indexName = "pyck-events-system"
	}

	err := s.postToQuickwit(indexName, events)
	if err != nil && isIndexNotFoundError(err) {
		if createErr := s.createIndex(indexName, tenantID); createErr != nil {
			return fmt.Errorf("failed to create index: %w", createErr)
		}

		return s.postToQuickwit(indexName, events)
	}

	return err
}

func (s *QuickwitSyncService) postToQuickwit(indexName string, events []any) error {
	// Create logger with index context
	logger := log.With().Str("index_name", indexName).Logger()

	url := fmt.Sprintf("%s/api/v1/%s/ingest", s.quickwitURL, indexName)

	var body bytes.Buffer
	var marshalErrors []error
	successfulEvents := 0

	for _, event := range events {
		jsonData, err := json.Marshal(event)
		if err != nil {
			marshalErrors = append(marshalErrors, err)
			logger.Error().Err(err).Interface("event", event).Msg("Failed to marshal event")
			continue
		}
		body.Write(jsonData)
		body.WriteByte('\n')
		successfulEvents++
	}

	if successfulEvents == 0 {
		return fmt.Errorf("%w: %d events", ErrAllEventsMarshalFailed, len(events))
	}

	if len(marshalErrors) > 0 {
		logger.Warn().Int("failed_count", len(marshalErrors)).Int("success_count", successfulEvents).Msg("Some events failed to marshal")
	}

	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("%w: status %d: %s", ErrQuickwitBadStatus, resp.StatusCode, string(respBody))
}

func (s *QuickwitSyncService) createIndex(indexName string, tenantID uuid.UUID) error {
	// Create logger with tenant context
	logger := log.With().Str("tenant_id", tenantID.String()).Str("index_name", indexName).Logger()
	indexConfig := map[string]any{
		"version":  "0.7",
		"index_id": indexName,
		"doc_mapping": map[string]any{
			"mode": "dynamic",
			"field_mappings": []map[string]any{
				{
					"name": "timestamp",
					"type": "datetime",
					"fast": true,
				},
				{
					"name":      "subject",
					"type":      "text",
					"tokenizer": "raw",
				},
				{
					"name":      "tenant_id",
					"type":      "text",
					"tokenizer": "raw",
					"fast":      true,
				},
				{
					"name":      "event_type",
					"type":      "text",
					"tokenizer": "raw",
					"fast":      true,
				},
				{
					"name":      "service",
					"type":      "text",
					"tokenizer": "default",
					"fast":      true,
				},
				{
					"name":      "schema",
					"type":      "text",
					"tokenizer": "default",
					"fast":      true,
				},
				{
					"name":      "operation",
					"type":      "text",
					"tokenizer": "raw",
					"fast":      true,
				},
				{
					"name":      "entity_id",
					"type":      "text",
					"tokenizer": "raw",
				},
				{
					"name":        "data",
					"type":        "json",
					"expand_dots": true,
					"tokenizer":   "default",
				},
				{
					"name":      "data_fulltext",
					"type":      "text",
					"tokenizer": "default",
				},
				{
					"name":      "namespace",
					"type":      "text",
					"tokenizer": "raw",
				},
				{
					"name": "sequence",
					"type": "u64",
					"fast": true,
				},
				{
					"name":      "stream_name",
					"type":      "text",
					"tokenizer": "raw",
				},
				{
					"name":      "workflow_assignee",
					"type":      "text",
					"tokenizer": "raw",
				},
				{
					"name":      "workflow_search_attributes",
					"type":      "json",
					"tokenizer": "default",
				},
			},
		},
		"search_settings": map[string]any{
			"default_search_fields": []string{"data_fulltext", "service", "schema"},
		},
		"indexing_settings": map[string]any{
			"commit_timeout_secs": 30,
		},
	}

	jsonData, err := json.Marshal(indexConfig)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/v1/indexes", s.quickwitURL)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		logger.Info().Msg("Created Quickwit index")
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("%w: status %d: %s", ErrIndexCreationFailed, resp.StatusCode, string(respBody))
}

func isIndexNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "404") || strings.Contains(errStr, "not found") || strings.Contains(errStr, "does not exist")
}

func extractTenantID(subject string) *uuid.UUID {
	parts := strings.Split(subject, ".")
	if len(parts) < 2 {
		return nil
	}

	for _, part := range parts {
		if id, err := uuid.Parse(part); err == nil {
			return &id
		}
	}

	return nil
}

// Shutdown gracefully stops the QuickwitSyncService
func (s *QuickwitSyncService) Shutdown() {
	log.Info().Msg("Shutting down QuickwitSyncService")

	if s.ticker != nil {
		s.ticker.Stop()
	}

	if s.shutdownCh != nil {
		close(s.shutdownCh)
	}

	// Flush any remaining batches
	s.flushAllBatches()
}
