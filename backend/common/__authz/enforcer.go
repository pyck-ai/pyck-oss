package authz

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
	"github.com/pyck-ai/pyck/backend/common/auth"
	"github.com/pyck-ai/pyck/backend/common/log"
)

//go:embed model.conf
var casbinModel string

// MemoryAdapter implements a simple in-memory adapter for Casbin
type MemoryAdapter struct {
	policies [][]string
	roles    [][]string
}

// NewMemoryAdapter creates a new in-memory adapter
func NewMemoryAdapter() *MemoryAdapter {
	return &MemoryAdapter{
		policies: make([][]string, 0),
		roles:    make([][]string, 0),
	}
}

// LoadPolicy loads all policies from memory
func (a *MemoryAdapter) LoadPolicy(model model.Model) error {
	// Load policies
	for _, policy := range a.policies {
		_ = persist.LoadPolicyLine(policy[0]+", "+policy[1]+", "+policy[2]+", "+policy[3]+", "+policy[4], model)
	}
	
	// Load roles
	for _, role := range a.roles {
		_ = persist.LoadPolicyLine("g, "+role[0]+", "+role[1]+", "+role[2], model)
	}
	
	return nil
}

// SavePolicy saves all policies to memory (not implemented for read-only use)
func (a *MemoryAdapter) SavePolicy(model model.Model) error {
	return nil
}

// AddPolicy adds a policy to memory
func (a *MemoryAdapter) AddPolicy(sec string, ptype string, rule []string) error {
	switch ptype {
	case "p":
		a.policies = append(a.policies, rule)
	case "g":
		a.roles = append(a.roles, rule)
	}
	return nil
}

// RemovePolicy removes a policy from memory
func (a *MemoryAdapter) RemovePolicy(sec string, ptype string, rule []string) error {
	if ptype == "p" {
		for i, policy := range a.policies {
			if len(policy) == len(rule) {
				match := true
				for j, r := range rule {
					if policy[j] != r {
						match = false
						break
					}
				}
				if match {
					a.policies = append(a.policies[:i], a.policies[i+1:]...)
					break
				}
			}
		}
	} else if ptype == "g" {
		for i, role := range a.roles {
			if len(role) == len(rule) {
				match := true
				for j, r := range rule {
					if role[j] != r {
						match = false
						break
					}
				}
				if match {
					a.roles = append(a.roles[:i], a.roles[i+1:]...)
					break
				}
			}
		}
	}
	return nil
}

// RemoveFilteredPolicy removes policies that match the filter
func (a *MemoryAdapter) RemoveFilteredPolicy(sec string, ptype string, fieldIndex int, fieldValues ...string) error {
	if ptype == "p" {
		newPolicies := make([][]string, 0)
		for _, policy := range a.policies {
			match := false
			for i, value := range fieldValues {
				if fieldIndex+i < len(policy) && policy[fieldIndex+i] == value {
					match = true
				} else {
					match = false
					break
				}
			}
			if !match {
				newPolicies = append(newPolicies, policy)
			}
		}
		a.policies = newPolicies
	} else if ptype == "g" {
		newRoles := make([][]string, 0)
		for _, role := range a.roles {
			match := false
			for i, value := range fieldValues {
				if fieldIndex+i < len(role) && role[fieldIndex+i] == value {
					match = true
				} else {
					match = false
					break
				}
			}
			if !match {
				newRoles = append(newRoles, role)
			}
		}
		a.roles = newRoles
	}
	return nil
}

// InitEnforcer initializes a new Casbin enforcer with the embedded model
func InitEnforcer(ctx context.Context) (*casbin.Enforcer, error) {
	// Create model from embedded string
	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, fmt.Errorf("failed to create model: %w", err)
	}

	// Create adapter
	adapter := NewMemoryAdapter()

	// Create enforcer
	enforcer, err := casbin.NewEnforcer(m, adapter)
	if err != nil {
		return nil, fmt.Errorf("failed to create enforcer: %w", err)
	}

	log.ForContext(ctx).Info().Msg("Casbin enforcer initialized successfully")
	return enforcer, nil
}

// EnforceWithEnforcer checks if a request is allowed using context to extract user and tenant information
func EnforceWithEnforcer(ctx context.Context, enforcer *casbin.Enforcer, resource, action string) (bool, error) {
	user := auth.ForContext(ctx)
	if user == nil {
		return false, fmt.Errorf("unauthorized: user not authenticated")
	}

	// Create logger from context with common fields
	logger := log.ForContext(ctx).With().
		Str("user_id", user.ID.String()).
		Str("tenant_id", user.TenantID.String()).
		Str("resource", resource).
		Str("action", action).
		Logger()

	// Service users bypass RBAC completely
	if auth.IsServiceUser(ctx) {
		logger.Debug().Msg("Service user access granted (bypassing Casbin RBAC)")
		return true, nil
	}

	userID := user.ID.String()
	tenantID := user.TenantID.String()

	// Check permission using Casbin
	allowed, err := enforcer.Enforce(userID, tenantID, resource, action)
	if err != nil {
		logger.Err(err).Msg("Error during authorization check")
		return false, err
	}

	logger.Debug().
		Bool("allowed", allowed).
		Msg("Authorization check completed")

	return allowed, nil
}