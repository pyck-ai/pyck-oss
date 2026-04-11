package resolvers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/pyck-ai/pyck/backend/common/test/mocks"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"

	registertenantwf "github.com/pyck-ai/pyck/backend/management/workflows/register-tenant"
)

// =============================================================================
// GRAPHQL TEMPLATES
// =============================================================================

var registerTenant = resolver.ParseTemplate(`mutation {
	registerTenant(input: {
		name: "{{.Name}}"
		adminUsername: "{{.AdminUsername}}"
		adminEmail: "{{.AdminEmail}}"
		adminFirstName: "{{.AdminFirstName}}"
		adminLastName: "{{.AdminLastName}}"
		adminPassword: "{{.AdminPassword}}"
	}) {
		success
	}
}`)

var registerTenantWithData = resolver.ParseTemplate(`mutation {
	registerTenant(input: {
		name: "{{.Name}}"
		adminUsername: "{{.AdminUsername}}"
		adminEmail: "{{.AdminEmail}}"
		adminFirstName: "{{.AdminFirstName}}"
		adminLastName: "{{.AdminLastName}}"
		adminPassword: "{{.AdminPassword}}"
		data: {flavour: "{{.Flavour}}"}
	}) {
		success
	}
}`)

// =============================================================================
// RESPONSE TYPES
// =============================================================================

type registerTenantData struct {
	RegisterTenant struct {
		Success bool
	}
}

// =============================================================================
// REGISTER TENANT TESTS
// =============================================================================

func TestRegisterTenant(t *testing.T) {
	t.Parallel()

	t.Run("registers tenant successfully", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		newTenantID := uuid.New()
		workflowOutput := registertenantwf.RegisterTenantWorkflowOutput{
			OrganizationID: "org-123",
			TenantID:       newTenantID,
			LoginName:      "testadmin",
			UserID:         uuid.New().String(),
			UserRoles:      []string{"admin"},
		}

		mockRun := mocks.NewMockWorkflowRun("workflow-id", "run-id", workflowOutput, nil)
		te.TemporalClient.
			On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(mockRun, nil).Once()
		te.TemporalClient.
			On("GetWorkflow", mock.Anything, "workflow-id", "run-id").
			Return(mockRun).Once()

		data := execOK[registerTenantData](te, ctx, registerTenant, map[string]any{
			"Name":           "Test Tenant",
			"AdminUsername":  "testadmin",
			"AdminEmail":     "testadmin@example.com",
			"AdminFirstName": "Test",
			"AdminLastName":  "Admin",
			"AdminPassword":  "SecurePass123!",
		})

		assert.True(t, data.RegisterTenant.Success)

		// Workflow execution doesn't emit synchronous entity events (workflow is mocked)
		te.assertNoEvents(ctx)
	})

	t.Run("passes flavour to workflow input", func(t *testing.T) {
		t.Parallel()
		te := setup(t)
		defer te.Close(t)
		ctx := te.ctx(userA)

		newTenantID := uuid.New()
		workflowOutput := registertenantwf.RegisterTenantWorkflowOutput{
			OrganizationID: "org-123",
			TenantID:       newTenantID,
			LoginName:      "testadmin",
			UserID:         uuid.New().String(),
			UserRoles:      []string{"admin"},
		}

		mockRun := mocks.NewMockWorkflowRun("workflow-id", "run-id", workflowOutput, nil)
		te.TemporalClient.
			On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.MatchedBy(func(args []interface{}) bool {
				if len(args) != 1 {
					return false
				}
				input, ok := args[0].(registertenantwf.RegisterTenantWorkflowInput)
				return ok && input.Data["flavour"] == "pyck-go"
			})).
			Return(mockRun, nil).Once()
		te.TemporalClient.
			On("GetWorkflow", mock.Anything, "workflow-id", "run-id").
			Return(mockRun).Once()

		data := execOK[registerTenantData](te, ctx, registerTenantWithData, map[string]any{
			"Name":           "Test Tenant",
			"AdminUsername":  "testadmin",
			"AdminEmail":     "testadmin@example.com",
			"AdminFirstName": "Test",
			"AdminLastName":  "Admin",
			"AdminPassword":  "SecurePass123!",
			"Flavour":        "pyck-go",
		})

		assert.True(t, data.RegisterTenant.Success)
		te.TemporalClient.AssertExpectations(t)
	})
}
