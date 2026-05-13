package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/google/uuid"
	"github.com/hasura/go-graphql-client"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pyck-ai/pyck/backend/common/events"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// AuthzCache manages isolated Casbin enforcers for each tenant
type AuthzCache struct {
	mu               sync.RWMutex
	enforcers        map[string]*TenantEnforcer // map[tenantID]*TenantEnforcer
	gatewayURL       string
	token            string
	consumer         jetstream.Consumer
	js               jetstream.JetStream
	serviceName      string
	streamName       string
	resourcePrefixes []string // Only cache policies for these resource prefixes
	maxCacheSize     int      // Maximum number of tenant enforcers to keep in memory
	evictionOrder    []string // LRU tracking for eviction
}

// buildEventSubjects constructs the list of event subjects to monitor based on configuration
func buildEventSubjects(ctx context.Context, options AuthzCacheOptions) []string {
	logger := log.ForContext(ctx)
	var subjects []string

	// If custom event subjects are provided, use them instead of defaults
	if len(options.EventSubjects) > 0 {
		subjects = append(subjects, options.EventSubjects...)
	}

	// Include management events by default unless explicitly disabled
	if len(options.EventSubjects) == 0 || options.IncludeManagementEvents {
		managementSubjects := []string{
			options.Stream + ".*.crud.management.role.*.*",
			options.Stream + ".*.crud.management.group.*.*",
			options.Stream + ".*.crud.management.policy.*.*",
		}
		subjects = append(subjects, managementSubjects...)
	}

	// If no subjects configured, fall back to management defaults for backward compatibility
	if len(subjects) == 0 {
		subjects = []string{
			options.Stream + ".*.crud.management.role.*.*",
			options.Stream + ".*.crud.management.group.*.*",
			options.Stream + ".*.crud.management.policy.*.*",
		}
	}

	logger.Info().
		Str("service", options.ServiceName).
		Strs("subjects", subjects).
		Msg("AuthzCache configured with event subjects")

	return subjects
}

// TenantEnforcer wraps a Casbin enforcer with metadata
type TenantEnforcer struct {
	enforcer     *casbin.Enforcer
	lastAccessed time.Time
	tenantID     string
}

// NewAuthzCache creates a new authorization cache instance
func NewAuthzCache(ctx context.Context, js jetstream.JetStream, options AuthzCacheOptions) (*AuthzCache, error) {
	logger := log.ForContext(ctx)

	// Build event subjects dynamically based on configuration
	filterSubjects := buildEventSubjects(ctx, options)

	// Create consumer for authorization events
	consumer, err := js.CreateOrUpdateConsumer(ctx, options.Stream, jetstream.ConsumerConfig{
		Name:              options.ServiceName + "Authz",
		FilterSubjects:    filterSubjects,
		InactiveThreshold: 10 * time.Minute,
	})
	if err != nil {
		logger.Err(err).Msg("creating authz consumer")
		return nil, err
	}

	logger.Info().Str("consumer", options.ServiceName+"Authz").Msg("consumer created")

	// Set default max cache size if not provided
	maxCacheSize := options.MaxCacheSize
	if maxCacheSize == 0 {
		maxCacheSize = 100 // Default to 100 tenant enforcers
	}

	return &AuthzCache{
		enforcers:        make(map[string]*TenantEnforcer),
		gatewayURL:       options.GatewayURL,
		token:            options.JwtToken,
		consumer:         consumer,
		js:               js,
		serviceName:      options.ServiceName,
		streamName:       options.Stream,
		resourcePrefixes: options.ResourcePrefixes,
		maxCacheSize:     maxCacheSize,
		evictionOrder:    make([]string, 0, maxCacheSize),
	}, nil
}

// validateUUID validates and returns a UUID from a string
func validateUUID(id string) (uuid.UUID, error) {
	parsedUUID, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid UUID format: %s", id)
	}
	return parsedUUID, nil
}

// GetEnforcerForTenant returns the Casbin enforcer for a specific tenant
// This is the main entry point for authorization checks
func (c *AuthzCache) GetEnforcerForTenant(ctx context.Context, tenantID string) (*casbin.Enforcer, error) {
	logger := log.ForContext(ctx)

	// Validate tenant ID format first
	if _, err := validateUUID(tenantID); err != nil {
		return nil, fmt.Errorf("invalid tenant ID: %w", err)
	}

	// Fast path: Read lock to check for existing enforcer
	c.mu.RLock()
	if _, exists := c.enforcers[tenantID]; exists {
		// Found cached enforcer, upgrade to write lock to update access time
		c.mu.RUnlock()
		c.mu.Lock()
		// Double-check the enforcer still exists after lock upgrade
		if tenantEnforcer, exists := c.enforcers[tenantID]; exists {
			tenantEnforcer.lastAccessed = time.Now()
			c.mu.Unlock()
			return tenantEnforcer.enforcer, nil
		}
		c.mu.Unlock()
		// Fall through to create a new enforcer if it was removed
	} else {
		c.mu.RUnlock()
	}

	// Slow path: Write lock to create the enforcer
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check in case another goroutine created it while we waited for the lock
	if tenantEnforcer, exists := c.enforcers[tenantID]; exists {
		tenantEnforcer.lastAccessed = time.Now()
		return tenantEnforcer.enforcer, nil
	}

	logger.Info().Str("tenant_id", tenantID).Msg("Cache miss. Loading policies for tenant")

	// Create and load enforcer for this tenant
	enforcer, err := c.createAndLoadEnforcerForTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to create enforcer for tenant %s: %w", tenantID, err)
	}

	// Store the enforcer
	c.enforcers[tenantID] = &TenantEnforcer{
		enforcer:     enforcer,
		lastAccessed: time.Now(),
		tenantID:     tenantID,
	}

	// Track for LRU eviction
	c.evictionOrder = append(c.evictionOrder, tenantID)

	// Evict oldest tenant if we're over the limit
	if len(c.enforcers) > c.maxCacheSize {
		c.evictOldestTenant()
	}

	return enforcer, nil
}

// createAndLoadEnforcerForTenant creates a new Casbin enforcer and loads policies for a specific tenant
func (c *AuthzCache) createAndLoadEnforcerForTenant(ctx context.Context, tenantID string) (*casbin.Enforcer, error) {
	// Create a new Casbin model (simplified for per-tenant isolation)
	m, err := model.NewModelFromString(`
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act, eft

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = g(r.sub, p.sub) && wildcardMatch(p.obj, r.obj) && wildcardMatch(p.act, r.act)
`)
	if err != nil {
		return nil, fmt.Errorf("failed to create casbin model: %w", err)
	}

	newEnforcer, err := casbin.NewEnforcer(m)
	if err != nil {
		return nil, fmt.Errorf("failed to create casbin enforcer: %w", err)
	}

	// Add custom wildcard matching function
	newEnforcer.AddFunction("wildcardMatch", func(args ...interface{}) (interface{}, error) {
		if len(args) != 2 {
			return false, fmt.Errorf("wildcardMatch requires exactly 2 arguments")
		}

		pattern, ok1 := args[0].(string)
		value, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return false, fmt.Errorf("wildcardMatch arguments must be strings")
		}

		// Handle wildcard patterns
		if pattern == "*" {
			return true, nil
		}

		// Exact match
		if pattern == value {
			return true, nil
		}

		// Pattern matching for "service.*" style wildcards
		if strings.HasSuffix(pattern, ".*") {
			prefix := strings.TrimSuffix(pattern, ".*")
			return strings.HasPrefix(value, prefix+"."), nil
		}

		return false, nil
	})

	header := map[string]string{
		// TODO(michael): Fix tenant context
		"Authorization":  "Bearer " + c.token,
		"pyck-tenant-id": uuid.Max.String(),
	}

	httpHeaderClient := json_schema.NewHTTPClientWithHeaders(nil, header)

	// Create GraphQL client with auth header
	client := graphql.NewClient(c.gatewayURL, httpHeaderClient)

	// Load policies for this tenant only
	if err := c.loadPoliciesForTenant(client, newEnforcer, tenantID); err != nil {
		return nil, fmt.Errorf("failed to load policies: %w", err)
	}

	// Load roles for this tenant only
	if err := c.loadRolesForTenant(client, newEnforcer, tenantID); err != nil {
		return nil, fmt.Errorf("failed to load roles: %w", err)
	}

	// Load groups for this tenant only
	if err := c.loadGroupsForTenant(client, newEnforcer, tenantID); err != nil {
		return nil, fmt.Errorf("failed to load groups: %w", err)
	}

	return newEnforcer, nil
}

// loadPoliciesForTenant loads policies for a specific tenant
func (c *AuthzCache) loadPoliciesForTenant(client *graphql.Client, enforcer *casbin.Enforcer, tenantID string) error {
	logger := log.DefaultLogger()

	// GraphQL query with tenant filter
	var query struct {
		Policies struct {
			Edges []struct {
				Node struct {
					ID       string `graphql:"id"`
					Resource string `graphql:"resource"`
					Action   string `graphql:"action"`
					Effect   string `graphql:"effect"`
					TenantID string `graphql:"tenantID"`
					Role     struct {
						ID string `graphql:"id"`
					} `graphql:"role"`
				} `graphql:"node"`
			} `graphql:"edges"`
		} `graphql:"policies(first: 1000, where: {tenantID: $tenantID})"`
	}

	variables := map[string]interface{}{
		"tenantID": tenantID,
	}

	err := client.Query(context.Background(), &query, variables)
	if err != nil {
		return err
	}

	// Add policies to enforcer (already filtered by tenant)
	filteredCount := 0
	for _, edge := range query.Policies.Edges {
		policy := edge.Node

		// Additional validation: ensure tenant ID matches
		if policy.TenantID != tenantID {
			logger.Error().
				Str("expected_tenant", tenantID).
				Str("actual_tenant", policy.TenantID).
				Msg("Tenant mismatch in policy - skipping for security")
			continue
		}

		// Filter by resource prefixes if configured
		if len(c.resourcePrefixes) > 0 {
			resourceMatches := false
			for _, prefix := range c.resourcePrefixes {
				if strings.HasPrefix(policy.Resource, prefix) {
					resourceMatches = true
					break
				}
			}
			if !resourceMatches {
				continue
			}
		}

		// Validate role ID
		if _, err := validateUUID(policy.Role.ID); err != nil {
			logger.Err(err).Str("role_id", policy.Role.ID).Msg("Invalid role ID in policy")
			continue
		}

		// Validate effect
		if policy.Effect != "allow" && policy.Effect != "deny" {
			logger.Error().Str("effect", policy.Effect).Msg("Invalid policy effect - must be 'allow' or 'deny'")
			continue
		}

		filteredCount++
		// Add policy to enforcer (tenant is implicit in this enforcer)
		_, err := enforcer.AddPolicy(policy.Role.ID, policy.Resource, policy.Action, policy.Effect)
		if err != nil {
			logger.Err(err).
				Str("role", policy.Role.ID).
				Str("resource", policy.Resource).
				Str("action", policy.Action).
				Msg("Failed to add policy to enforcer")
		}
	}

	logger.Info().
		Str("tenant_id", tenantID).
		Int("count", filteredCount).
		Msg("Loaded policies for tenant")
	return nil
}

// loadRolesForTenant loads roles and their assignments for a specific tenant
func (c *AuthzCache) loadRolesForTenant(client *graphql.Client, enforcer *casbin.Enforcer, tenantID string) error {
	var query struct {
		Roles struct {
			Edges []struct {
				Node struct {
					ID       string `graphql:"id"`
					Name     string `graphql:"name"`
					TenantID string `graphql:"tenantID"`
					Users    struct {
						Edges []struct {
							Node struct {
								ID string `graphql:"id"`
							} `graphql:"node"`
						} `graphql:"edges"`
					} `graphql:"users"`
					Groups struct {
						Edges []struct {
							Node struct {
								ID string `graphql:"id"`
							} `graphql:"node"`
						} `graphql:"edges"`
					} `graphql:"groups"`
				} `graphql:"node"`
			} `graphql:"edges"`
		} `graphql:"roles(first: 1000, where: {tenantID: $tenantID})"`
	}

	variables := map[string]interface{}{
		"tenantID": tenantID,
	}

	err := client.Query(context.Background(), &query, variables)
	if err != nil {
		return err
	}

	// Add role assignments to enforcer
	for _, edge := range query.Roles.Edges {
		role := edge.Node

		// Additional validation: ensure tenant ID matches
		if role.TenantID != tenantID {
			log.DefaultLogger().Error().
				Str("expected_tenant", tenantID).
				Str("actual_tenant", role.TenantID).
				Msg("Tenant mismatch in role - skipping for security")
			continue
		}

		// Validate role ID
		if _, err := validateUUID(role.ID); err != nil {
			log.DefaultLogger().Error().Err(err).Str("role_id", role.ID).Msg("Invalid role ID")
			continue
		}

		// Add direct user-role assignments
		for _, userEdge := range role.Users.Edges {
			if _, err := validateUUID(userEdge.Node.ID); err != nil {
				log.DefaultLogger().Error().Err(err).Str("user_id", userEdge.Node.ID).Msg("Invalid user ID in role assignment")
				continue
			}
			_, err := enforcer.AddGroupingPolicy(userEdge.Node.ID, role.ID)
			if err != nil {
				log.DefaultLogger().Error().Err(err).
					Str("user", userEdge.Node.ID).
					Str("role", role.ID).
					Msg("Failed to add user-role assignment")
			}
		}

		// Add group-role assignments
		for _, groupEdge := range role.Groups.Edges {
			if _, err := validateUUID(groupEdge.Node.ID); err != nil {
				log.DefaultLogger().Error().Err(err).Str("group_id", groupEdge.Node.ID).Msg("Invalid group ID in role assignment")
				continue
			}
			_, err := enforcer.AddGroupingPolicy(groupEdge.Node.ID, role.ID)
			if err != nil {
				log.DefaultLogger().Error().Err(err).
					Str("group", groupEdge.Node.ID).
					Str("role", role.ID).
					Msg("Failed to add group-role assignment")
			}
		}
	}

	log.DefaultLogger().Info().
		Str("tenant_id", tenantID).
		Int("count", len(query.Roles.Edges)).
		Msg("Loaded roles for tenant")
	return nil
}

// loadGroupsForTenant loads groups and their memberships for a specific tenant
func (c *AuthzCache) loadGroupsForTenant(client *graphql.Client, enforcer *casbin.Enforcer, tenantID string) error {
	var query struct {
		Groups struct {
			Edges []struct {
				Node struct {
					ID       string `graphql:"id"`
					Name     string `graphql:"name"`
					TenantID string `graphql:"tenantID"`
					Users    struct {
						Edges []struct {
							Node struct {
								ID string `graphql:"id"`
							} `graphql:"node"`
						} `graphql:"edges"`
					} `graphql:"users"`
				} `graphql:"node"`
			} `graphql:"edges"`
		} `graphql:"groups(first: 1000, where: {tenantID: $tenantID})"`
	}

	variables := map[string]interface{}{
		"tenantID": tenantID,
	}

	err := client.Query(context.Background(), &query, variables)
	if err != nil {
		return err
	}

	// Add user-group assignments to enforcer
	for _, edge := range query.Groups.Edges {
		group := edge.Node

		// Additional validation: ensure tenant ID matches
		if group.TenantID != tenantID {
			log.DefaultLogger().Error().
				Str("expected_tenant", tenantID).
				Str("actual_tenant", group.TenantID).
				Msg("Tenant mismatch in group - skipping for security")
			continue
		}

		// Validate group ID
		if _, err := validateUUID(group.ID); err != nil {
			log.DefaultLogger().Error().Err(err).Str("group_id", group.ID).Msg("Invalid group ID")
			continue
		}

		// Add user-group assignments
		for _, userEdge := range group.Users.Edges {
			if _, err := validateUUID(userEdge.Node.ID); err != nil {
				log.DefaultLogger().Error().Err(err).Str("user_id", userEdge.Node.ID).Msg("Invalid user ID in group membership")
				continue
			}
			_, err := enforcer.AddGroupingPolicy(userEdge.Node.ID, group.ID)
			if err != nil {
				log.DefaultLogger().Error().Err(err).
					Str("user", userEdge.Node.ID).
					Str("group", group.ID).
					Msg("Failed to add user-group assignment")
			}
		}
	}

	log.DefaultLogger().Info().
		Str("tenant_id", tenantID).
		Int("count", len(query.Groups.Edges)).
		Msg("Loaded groups for tenant")
	return nil
}

// evictOldestTenant removes the least recently used tenant enforcer
func (c *AuthzCache) evictOldestTenant() {
	if len(c.evictionOrder) == 0 {
		return
	}

	// Find the oldest tenant
	var oldestTenant string
	oldestTime := time.Now()

	for _, tenantID := range c.evictionOrder {
		if enforcer, exists := c.enforcers[tenantID]; exists {
			if enforcer.lastAccessed.Before(oldestTime) {
				oldestTime = enforcer.lastAccessed
				oldestTenant = tenantID
			}
		}
	}

	if oldestTenant != "" {
		delete(c.enforcers, oldestTenant)
		// Remove from eviction order
		newOrder := make([]string, 0, len(c.evictionOrder)-1)
		for _, id := range c.evictionOrder {
			if id != oldestTenant {
				newOrder = append(newOrder, id)
			}
		}
		c.evictionOrder = newOrder
		log.DefaultLogger().Info().Str("tenant_id", oldestTenant).Msg("Evicted tenant enforcer from cache")
	}
}

// InvalidateTenant removes a tenant's enforcer from the cache
func (c *AuthzCache) InvalidateTenant(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.enforcers, tenantID)
	// Remove from eviction order
	newOrder := make([]string, 0, len(c.evictionOrder)-1)
	for _, id := range c.evictionOrder {
		if id != tenantID {
			newOrder = append(newOrder, id)
		}
	}
	c.evictionOrder = newOrder

	log.DefaultLogger().Info().Str("tenant_id", tenantID).Msg("Invalidated tenant cache")
}

// Init initializes the cache by starting the event listener
func (c *AuthzCache) Init(ctx context.Context) error {
	logger := log.ForContext(ctx)
	logger.Info().Msg("Initializing authorization cache...")

	// Start listening for events
	go c.listenToEvents()

	// Publish a cache invalidation channel subscription
	go c.subscribeToCacheInvalidations()

	logger.Info().Msg("Authorization cache initialized successfully")
	return nil
}

// listenToEvents listens for NATS events and invalidates affected tenant caches
func (c *AuthzCache) listenToEvents() {
	log.DefaultLogger().Info().Msg("Starting to listen for authorization events...")

	ctx := context.Background()
	for {
		messages, err := c.consumer.Fetch(10)
		if err != nil {
			log.DefaultLogger().Error().Err(err).Msg("Failed to fetch messages")
			time.Sleep(5 * time.Second)
			continue
		}

		for msg := range messages.Messages() {
			msgCtx := events.ContextFromJetstreamMessage(ctx, msg)
			if err := c.processEvent(msgCtx, msg); err != nil {
				log.ForContext(msgCtx).Error().Err(err).Str("subject", msg.Subject()).Msg("Failed to process event")
			}
			_ = msg.Ack()
		}

		if messages.Error() != nil {
			log.DefaultLogger().Error().Err(messages.Error()).Msg("Error in message iterator")
		}
	}
}

// subscribeToCacheInvalidations listens for cache invalidation broadcasts from other instances
func (c *AuthzCache) subscribeToCacheInvalidations() {
	subject := fmt.Sprintf("%s.cache.invalidate", c.streamName)

	// Create or get the stream for cache invalidations
	streamConfig := jetstream.StreamConfig{
		Name:     c.streamName + "_CACHE",
		Subjects: []string{subject},
	}

	stream, err := c.js.CreateOrUpdateStream(context.Background(), streamConfig)
	if err != nil {
		log.DefaultLogger().Error().Err(err).Msg("Failed to create/update cache invalidation stream")
		return
	}

	// Create consumer for cache invalidation messages
	consumer, err := stream.CreateOrUpdateConsumer(context.Background(), jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckExplicitPolicy,
		InactiveThreshold: 10 * time.Minute,
	})
	if err != nil {
		log.DefaultLogger().Error().Err(err).Msg("Failed to create consumer for cache invalidations")
		return
	}

	log.DefaultLogger().Info().Str("subject", subject).Msg("Subscribed to cache invalidation messages")

	for {
		// Fetch a single message with timeout
		messages, err := consumer.Fetch(1, jetstream.FetchMaxWait(10*time.Second))
		if err != nil {
			log.DefaultLogger().Error().Err(err).Msg("Error fetching cache invalidation messages")
			time.Sleep(1 * time.Second)
			continue
		}

		// Process the message
		for msg := range messages.Messages() {
			msgCtx := events.ContextFromJetstreamMessage(context.Background(), msg)

			var invalidation struct {
				TenantID  string    `json:"tenant_id"`
				ServiceID string    `json:"service_id"`
				Timestamp time.Time `json:"timestamp"`
			}

			if err := json.Unmarshal(msg.Data(), &invalidation); err != nil {
				log.ForContext(msgCtx).Error().Err(err).Msg("Failed to unmarshal cache invalidation")
				_ = msg.Ack()
				continue
			}

			// Don't invalidate if it's from our own service
			if invalidation.ServiceID != c.serviceName {
				c.InvalidateTenant(invalidation.TenantID)
			}

			_ = msg.Ack()
		}

		// Check for errors in the message batch
		if messages.Error() != nil {
			log.DefaultLogger().Error().Err(messages.Error()).Msg("Error in cache invalidation message batch")
		}
	}
}

// broadcastCacheInvalidation sends a cache invalidation message to other instances
func (c *AuthzCache) broadcastCacheInvalidation(tenantID string) error {
	subject := fmt.Sprintf("%s.cache.invalidate", c.streamName)

	invalidation := struct {
		TenantID  string    `json:"tenant_id"`
		ServiceID string    `json:"service_id"`
		Timestamp time.Time `json:"timestamp"`
	}{
		TenantID:  tenantID,
		ServiceID: c.serviceName,
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(invalidation)
	if err != nil {
		return fmt.Errorf("failed to marshal invalidation: %w", err)
	}

	_, err = c.js.Publish(context.Background(), subject, data)
	if err != nil {
		return fmt.Errorf("failed to publish invalidation: %w", err)
	}

	return nil
}

// processEvent processes a single NATS event and invalidates the affected tenant's cache
func (c *AuthzCache) processEvent(ctx context.Context, msg jetstream.Msg) error {
	var event events.MutationEventMessage
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		return fmt.Errorf("failed to unmarshal event: %w", err)
	}

	// Invalidate the tenant's cache
	c.InvalidateTenant(event.TenantID.String())

	// Broadcast invalidation to other service instances
	if err := c.broadcastCacheInvalidation(event.TenantID.String()); err != nil {
		log.DefaultLogger().Error().Err(err).Str("tenant_id", event.TenantID.String()).Msg("Failed to broadcast cache invalidation")
	}

	return nil
}

// GetEnforcer returns the global enforcer (deprecated - use GetEnforcerForTenant)
func (c *AuthzCache) GetEnforcer() *casbin.Enforcer {
	log.DefaultLogger().Warn().Msg("GetEnforcer() is deprecated - use GetEnforcerForTenant() for proper tenant isolation")
	return nil
}
