// Package searchattributes defines the Temporal workflow search attribute keys.
// This is an internal package to avoid circular dependencies between
// common/events, common/workflow, and common/json-schema.
package searchattributes

// Search attribute key constants for Temporal workflows.
const (
	PyckDataIDKey           = "pyck_data_id"
	PyckWorkflowAssigneeKey = "pyck_workflow_assignee"
	PyckWorkflowNameKey     = "pyck_workflow_name"
	PyckTenantIDKey         = "pyck_tenant_id"
	PyckDataTypeKey         = "pyck_data_type"
	PyckServiceKey          = "pyck_service"
	PyckGroupByKey          = "pyck_group_by"
)
