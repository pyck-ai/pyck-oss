/*
Package authz provides a distributed authorization system using Casbin for role-based access control (RBAC) with multi-tenancy support.

Architecture

The authorization system consists of:
  - Management Service: Central storage and management of roles, groups, and policies via GraphQL
  - Common/authz Package: Local Casbin enforcer with event-driven cache synchronization
  - NATS Events: Real-time synchronization of authorization changes across services

Current Implementation Status

Implemented:
  - Core authorization engine with Casbin
  - NATS JetStream cache synchronization
  - Management service fully protected with authorization
  - Tenant onboarding with default roles
  - JWT authentication integration

In Progress:
  - Integration with other services (inventory, file, workflow)
  - Bootstrap service for system roles
  - Production-ready examples

Usage

Initialize the authorization cache in your service's main function:

	package main

	import (
	    "context"
	    "log"
	    "time"
	    
	    "github.com/nats-io/nats.go/jetstream"
	    "github.com/pyck-ai/pyck/backend/common/authz"
	)

	func main() {
	    // Setup NATS JetStream connection
	    js, err := setupJetStream()
	    if err != nil {
	        log.Fatal(err)
	    }

	    // Initialize authorization cache
	    err = authz.Initialize(js, authz.AuthzCacheOptions{
	        ServiceName:      "inventory",     // Your service name
	        ResourcePrefixes: []string{"inventory"}, // Only load inventory policies
	        GatewayURL:       "http://gateway:8080",
	        JwtToken:         "service-token",
	        Stream:           "PYCK_EVENTS",
	        MaxCacheSize:     100,  // Maximum number of tenant enforcers to cache
	    })
	    if err != nil {
	        log.Fatal("Failed to initialize authz:", err)
	    }

	    // Your service initialization code...
	}

Check permissions in your resolvers or handlers:

	func (r *queryResolver) Items(ctx context.Context) ([]*ent.Item, error) {
	    // Check if user can read inventory items
	    allowed, err := authz.Enforce(ctx, "inventory.item", "read")
	    if err != nil {
	        return nil, err
	    }
	    if !allowed {
	        return nil, fmt.Errorf("insufficient permissions")
	    }

	    // Proceed with the query...
	    return r.client.Item.Query().All(ctx)
	}

Resource Naming Convention

Use dot notation for resource names to create a hierarchical structure:
  - inventory.item - Inventory items
  - inventory.repository - Inventory repositories
  - picking.order - Picking orders
  - management.user - User management
  - management.company - Company management

Standard Actions

  - read - View/query resources
  - write - Create/update resources
  - delete - Delete resources
  - admin - Administrative actions

Multi-Service Configuration

The AuthzCache supports service-specific event subject configuration, allowing each service to:
  - Define custom event subjects to monitor
  - Include/exclude management service events
  - Scale authorization cache based on service needs

Configuration Options

AuthzCacheOptions structure:

	type AuthzCacheOptions struct {
	    GatewayURL       string   // Management service gateway URL
	    JwtToken         string   // Service JWT token
	    Stream           string   // NATS stream name
	    ServiceName      string   // Service identifier
	    ResourcePrefixes []string // Resource prefixes to cache (e.g., ["inventory"])
	    MaxCacheSize     int      // Max tenant enforcers in memory
	    
	    // Service-specific event configuration
	    EventSubjects           []string // Custom event subjects to monitor
	    IncludeManagementEvents bool     // Include standard management events (default: true)
	}

Service Configuration Examples

Management Service (uses direct database access, not AuthzCache):

	// This is the authoritative source for authorization data
	authorizer := authz.NewManagementAuthorizer(client)

Inventory Service Configuration:

	authzOptions := authz.AuthzCacheOptions{
	    GatewayURL:       config.GatewayUrl,
	    JwtToken:         config.ServiceToken,
	    Stream:           config.NatsStreamName,
	    ServiceName:      "inventory",
	    ResourcePrefixes: []string{"inventory"},
	    MaxCacheSize:     200, // Higher cache for warehouse operations
	    
	    // Monitor inventory-specific RBAC events
	    EventSubjects: []string{
	        config.NatsStreamName + ".*.crud.inventory.warehouse.*.*",
	        config.NatsStreamName + ".*.crud.inventory.location.*.*",
	        config.NatsStreamName + ".*.crud.inventory.zone.*.*",
	    },
	    IncludeManagementEvents: true, // Still need role/group updates
	}

Picking Service Configuration (with cross-service permissions):

	authzOptions := authz.AuthzCacheOptions{
	    GatewayURL:       config.GatewayUrl,
	    JwtToken:         config.ServiceToken,
	    Stream:           config.NatsStreamName,
	    ServiceName:      "picking",
	    ResourcePrefixes: []string{"picking", "inventory.item"}, // Cross-service permissions
	    MaxCacheSize:     50, // Smaller cache
	    
	    // Monitor picking-specific and inventory item events
	    EventSubjects: []string{
	        config.NatsStreamName + ".*.crud.picking.order.*.*",
	        config.NatsStreamName + ".*.crud.picking.task.*.*",
	        config.NatsStreamName + ".*.crud.inventory.item.*.*", // Cross-service
	    },
	    IncludeManagementEvents: true,
	}

Performance Optimization

Services can specify which resource prefixes they care about to only load relevant policies:

	// Inventory service only loads policies for inventory resources
	authz.Initialize(js, authz.AuthzCacheOptions{
	    ResourcePrefixes: []string{"inventory"},
	    // ... other options
	})

	// Gateway or multi-service can load all policies
	authz.Initialize(js, authz.AuthzCacheOptions{
	    ResourcePrefixes: nil, // Load all policies
	    // ... other options
	})

This optimization significantly reduces memory usage and initialization time for services that only need specific resource types.

Event System

The authorization cache automatically stays synchronized through NATS events:
  - Role changes trigger updates to user permissions
  - Policy changes immediately affect authorization decisions
  - Group membership changes update user roles

No manual cache invalidation is required.

Event Subject Patterns

Standard patterns:
  - {stream}.*.crud.{service}.{entity}.*.{operation}
  - Example: pyck.dev.crud.inventory.warehouse.create

Supported operations:
  - create - Entity creation
  - update - Entity modification
  - delete - Entity deletion

Best Practices

  1. Resource Naming: Use service prefix (e.g., inventory.warehouse)
  2. Event Subjects: Be specific to avoid unnecessary cache invalidations
  3. Cache Size: Set based on expected tenant count
  4. Cross-Service: Only cache resources your service actually checks
  5. Tenant Isolation: All policies are automatically scoped by tenant ID

Policy Management

Policies are managed through the Management Service GraphQL API:

	# Create a role
	mutation {
	  createRole(input: {
	    name: "InventoryManager"
	    description: "Can manage inventory items"
	    tenantID: "tenant-uuid"
	  }) {
	    role {
	      id
	      name
	    }
	  }
	}

	# Create a policy
	mutation {
	  createPolicy(input: {
	    resource: "inventory.item"
	    action: "write"
	    effect: "allow"
	    roleID: "role-uuid"
	    tenantID: "tenant-uuid"
	  }) {
	    policy {
	      id
	      resource
	      action
	    }
	  }
	}

Testing

The authz package includes comprehensive tests covering all functionality:

	# Run all tests
	go test ./backend/common/authz

	# Run tests with coverage
	go test -cover ./backend/common/authz

	# Run benchmarks
	go test -bench=. ./backend/common/authz

Test Structure:
  - enforcer_test.go - Tests for Casbin enforcer and memory adapter
  - cache_test.go - Tests for cache functionality and event processing
  - authz_test.go - Tests for global API and integration scenarios
  - benchmark_test.go - Performance benchmarks
  - setup_test.go - Test utilities and shared setup functions

Troubleshooting

  1. Authorization cache not initialized: Ensure authz.Initialize() is called during service startup
  2. User not authenticated: Verify the auth middleware is properly setting user context
  3. Permissions not updated: Check NATS connectivity and event processing logs
  4. Performance issues: Monitor Casbin enforcer performance and consider policy optimization
  5. Access always denied: Check if user has roles assigned and roles have policies

Monitoring

The authorization system logs all enforcement decisions at DEBUG level and errors at ERROR level. Monitor these logs for:
  - Failed authorization attempts
  - Policy loading errors
  - Event processing failures
  - Performance bottlenecks
  - Service user access (logged separately)

Backward Compatibility

  - Existing services continue working without changes
  - Default behavior preserves management-only event monitoring
  - Services can opt-in to enhanced configuration incrementally
*/
package authz