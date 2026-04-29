package workflow

import (
	"go.temporal.io/sdk/temporal"

	"github.com/pyck-ai/pyck/backend/common/internal/searchattributes"
)

// Re-export key constants for public API.
const (
	PyckDataIDKey               = searchattributes.PyckDataIDKey
	PyckWorkflowAssigneeKey     = searchattributes.PyckWorkflowAssigneeKey
	PyckWorkflowIsAssignableKey = searchattributes.PyckWorkflowIsAssignableKey
	PyckWorkflowNameKey         = searchattributes.PyckWorkflowNameKey
	PyckWorkflowTargetsKey      = searchattributes.PyckWorkflowTargetsKey
	PyckTenantIDKey             = searchattributes.PyckTenantIDKey
	PyckDataTypeKey             = searchattributes.PyckDataTypeKey
	PyckServiceKey              = searchattributes.PyckServiceKey
	PyckGroupByKey              = searchattributes.PyckGroupByKey
)

var (
	PyckDataID               = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckDataIDKey)
	PyckWorkflowAssignee     = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckWorkflowAssigneeKey)
	PyckWorkflowIsAssignable = temporal.NewSearchAttributeKeyBool(searchattributes.PyckWorkflowIsAssignableKey)
	PyckWorkflowName         = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckWorkflowNameKey)
	PyckWorkflowTargets      = temporal.NewSearchAttributeKeyKeywordList(searchattributes.PyckWorkflowTargetsKey)
	PyckTenantID             = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckTenantIDKey)
	PyckDataType             = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckDataTypeKey)
	PyckService              = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckServiceKey)
	PyckGroupBy              = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckGroupByKey)
)

var SearchAttributes = []temporal.SearchAttributeKey{
	PyckDataID,
	PyckWorkflowAssignee,
	PyckWorkflowIsAssignable,
	PyckWorkflowName,
	PyckWorkflowTargets,
	PyckTenantID,
	PyckDataType,
	PyckService,
	PyckGroupBy,
}
