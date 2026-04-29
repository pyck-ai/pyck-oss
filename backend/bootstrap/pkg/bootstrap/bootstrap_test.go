package bootstrap

import (
	"context"
	"testing"

	"github.com/pyck-ai/pyck/backend/bootstrap/internal/exporters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadConfig verifies that all YAML field names and values are correctly
// deserialized across the entire BootstrapConfig hierarchy.
func TestLoadConfig(t *testing.T) {
	t.Parallel()

	configBytes := []byte(`
zitadel:
  organizations:
  - name: TestOrg
    human_users:
    - username: testuser
      email: test@example.com
      first_name: Test
      last_name: User
      display_name: Test User
      password: secret123
      is_email_verified: true
      change_password: false
      role:
      - IAM_OWNER
      - IAM_ADMIN
    projects:
    - name: TestProject
      apps:
      - name: TestApp
        exports:
        - type: file
          file: app-creds.yaml
          name: APP_KEY
      roles:
      - key: admin
        display_name: Administrator
      - key: reader
        display_name: Read Only
      exports:
      - type: env
        file: project.env
        name: PROJECT_ID
        field: id
    project_grants:
    - organization_name: GrantOrg
      project_name: GrantedProject
      role_keys:
      - reader
      - admin
    machine_users:
    - username: svc-account
      name: Service Account
      access_token_type: jwt
      user_grants:
      - organization_name: TestOrg
        project_name: TestProject
        role_key: admin
      exports:
      - type: k8s
        file: secret.yaml
        name: SVC_TOKEN
    exports:
    - type: process-env
      file: org.env
      name: ORG_ID
      field: id
`)

	loadedConfig, err := loadConfig(context.Background(), "", configBytes)
	require.NoError(t, err)

	// Organization
	require.Len(t, loadedConfig.Zitadel.Organizations, 1)
	org := loadedConfig.Zitadel.Organizations[0]
	assert.Equal(t, "TestOrg", org.Name)

	// HumanUsers
	require.Len(t, org.HumanUsers, 1)
	hu := org.HumanUsers[0]
	assert.Equal(t, "testuser", hu.Username)
	assert.Equal(t, "test@example.com", hu.Email)
	assert.Equal(t, "Test", hu.FirstName)
	assert.Equal(t, "User", hu.LastName)
	assert.Equal(t, "Test User", hu.DisplayName)
	assert.Equal(t, "secret123", hu.Password)
	assert.True(t, hu.IsEmailVerified)
	assert.False(t, hu.ChangePassword)
	assert.Equal(t, []string{"IAM_OWNER", "IAM_ADMIN"}, hu.Role)

	// Projects
	require.Len(t, org.Projects, 1)
	proj := org.Projects[0]
	assert.Equal(t, "TestProject", proj.Name)

	// Apps
	require.Len(t, proj.Apps, 1)
	app := proj.Apps[0]
	assert.Equal(t, "TestApp", app.Name)
	require.Len(t, app.Exports, 1)
	assert.Equal(t, exporters.ExportTypeFile, app.Exports[0].Type)
	assert.Equal(t, "app-creds.yaml", app.Exports[0].File)
	assert.Equal(t, "APP_KEY", app.Exports[0].Name)

	// Roles
	require.Len(t, proj.Roles, 2)
	assert.Equal(t, "admin", proj.Roles[0].Key)
	assert.Equal(t, "Administrator", proj.Roles[0].DisplayName)
	assert.Equal(t, "reader", proj.Roles[1].Key)
	assert.Equal(t, "Read Only", proj.Roles[1].DisplayName)

	// Project Exports
	require.Len(t, proj.Exports, 1)
	assert.Equal(t, exporters.ExportTypeEnv, proj.Exports[0].Type)
	assert.Equal(t, "project.env", proj.Exports[0].File)
	assert.Equal(t, "PROJECT_ID", proj.Exports[0].Name)
	assert.Equal(t, "id", proj.Exports[0].Field)

	// ProjectGrants
	require.Len(t, org.ProjectGrants, 1)
	pg := org.ProjectGrants[0]
	assert.Equal(t, "GrantOrg", pg.OrganizationName)
	assert.Equal(t, "GrantedProject", pg.ProjectName)
	assert.Equal(t, []string{"reader", "admin"}, pg.RoleKeys)

	// MachineUsers
	require.Len(t, org.MachineUsers, 1)
	mu := org.MachineUsers[0]
	assert.Equal(t, "svc-account", mu.Username)
	assert.Equal(t, "Service Account", mu.Name)
	assert.Equal(t, "jwt", mu.AccessTokenType)

	// UserGrants
	require.Len(t, mu.UserGrants, 1)
	ug := mu.UserGrants[0]
	assert.Equal(t, "TestOrg", ug.OrganizationName)
	assert.Equal(t, "TestProject", ug.ProjectName)
	assert.Equal(t, "admin", ug.RoleKey)

	// MachineUser Exports
	require.Len(t, mu.Exports, 1)
	assert.Equal(t, exporters.ExportTypeK8s, mu.Exports[0].Type)
	assert.Equal(t, "secret.yaml", mu.Exports[0].File)
	assert.Equal(t, "SVC_TOKEN", mu.Exports[0].Name)

	// Organization Exports
	require.Len(t, org.Exports, 1)
	assert.Equal(t, exporters.ExportTypeProcessEnv, org.Exports[0].Type)
	assert.Equal(t, "org.env", org.Exports[0].File)
	assert.Equal(t, "ORG_ID", org.Exports[0].Name)
	assert.Equal(t, "id", org.Exports[0].Field)
}
