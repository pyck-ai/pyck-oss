// Package searchattributes defines the Temporal workflow search attribute keys.
// This is an internal package to avoid circular dependencies between
// common/events, common/workflow, and common/json-schema.
package searchattributes

// Search attribute key constants for Temporal workflows.
const (
	PyckDataIDKey               = "pyck_data_id"
	PyckWorkflowAssigneeKey     = "pyck_workflow_assignee"
	PyckWorkflowIsAssignableKey = "pyck_workflow_is_assignable"
	PyckWorkflowNameKey         = "pyck_workflow_name"
	PyckWorkflowTargetsKey      = "pyck_workflow_targets"
	PyckTenantIDKey             = "pyck_tenant_id"
	PyckDataTypeKey             = "pyck_data_type"
	PyckServiceKey              = "pyck_service"
	PyckGroupByKey              = "pyck_group_by"
	PyckTitleKey                = "pyck_title"
	PyckGroupTitleKey           = "pyck_group_title"
	PyckSortKeyKey              = "pyck_sort_key"
)
