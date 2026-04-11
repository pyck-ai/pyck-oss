package authz

import (
	"context"
	"fmt"
	"sync"

	"github.com/casbin/casbin/v2"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pyck-ai/pyck/backend/common/auth"
	"github.com/pyck-ai/pyck/backend/common/log"
)

var (
	// Global instance of the authorization cache
	globalAuthzCache *AuthzCache
	initOnce         sync.Once
)

// Initialize initializes the global authorization cache
func Initialize(ctx context.Context, js jetstream.JetStream, options AuthzCacheOptions) (err error) {
	logger := log.ForContext(ctx)

	initOnce.Do(func() {
		globalAuthzCache, err = NewAuthzCache(ctx, js, options)
		if err != nil {
			logger.Err(err).Msg("Failed to create authorization cache")
			return
		}

		if err = globalAuthzCache.Init(ctx); err != nil {
			logger.Err(err).Msg("Failed to initialize authorization cache")
			return
		}

		logger.Info().Msg("Global authorization cache initialized")
	})
	return err
}

// Enforce is the main public API for authorization checks
// It extracts user and tenant information from the context and checks permissions
func Enforce(ctx context.Context, resource, action string) (bool, error) {
	logger := log.ForContext(ctx)

	if globalAuthzCache == nil {
		logger.Error().Msg("Authorization cache not initialized")
		return false, ErrNotInitialized
	}

	// Extract tenant ID from context
	user := auth.ForContext(ctx)
	if user == nil {
		return false, fmt.Errorf("unauthorized: user not authenticated")
	}

	// Service users need explicit permissions for internal operations
	// They should not bypass RBAC completely for security reasons
	if auth.IsServiceUser(ctx) {
		logger.Debug().
			Str("user_id", user.ID.String()).
			Str("resource", resource).
			Str("action", action).
			Msg("Service user authorization check")
		// Service users will go through normal RBAC flow
	}

	// Get tenant-specific enforcer
	tenantID := user.TenantID.String()
	enforcer, err := globalAuthzCache.GetEnforcerForTenant(ctx, tenantID)
	if err != nil {
		logger.Err(err).Str("tenant_id", tenantID).Msg("Failed to get enforcer for tenant")
		return false, fmt.Errorf("failed to get tenant enforcer: %w", err)
	}

	// Use simplified enforcement without tenant ID in the params (tenant is implicit)
	userID := user.ID.String()
	allowed, err := enforcer.Enforce(userID, resource, action)
	if err != nil {
		logger.Err(err).
			Str("user", userID).
			Str("tenant", tenantID).
			Str("resource", resource).
			Str("action", action).
			Msg("Error during authorization check")
		return false, err
	}

	logger.Debug().
		Str("user", userID).
		Str("tenant", tenantID).
		Str("resource", resource).
		Str("action", action).
		Bool("allowed", allowed).
		Msg("Authorization check completed")

	return allowed, nil
}

// GetEnforcer returns the global Casbin enforcer instance
// This is useful for advanced use cases where direct enforcer access is needed
func GetEnforcer() *casbin.Enforcer {
	logger := log.DefaultLogger()
	if globalAuthzCache == nil {
		logger.Error().Msg("Authorization cache not initialized")
		return nil
	}
	return globalAuthzCache.GetEnforcer()
}

// GetCache returns the global authorization cache instance
// This is useful for advanced use cases or testing
func GetCache() *AuthzCache {
	return globalAuthzCache
}

// Custom errors
var (
	ErrNotInitialized = fmt.Errorf("authorization cache not initialized")
)