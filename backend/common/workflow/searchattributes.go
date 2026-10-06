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
	PyckTitleKey                = searchattributes.PyckTitleKey
	PyckGroupTitleKey           = searchattributes.PyckGroupTitleKey
	PyckSortKeyKey              = searchattributes.PyckSortKeyKey
	PyckTransactionIDKey        = searchattributes.PyckTransactionIDKey
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
	PyckTitle                = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckTitleKey)
	PyckGroupTitle           = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckGroupTitleKey)
	PyckSortKey              = temporal.NewSearchAttributeKeyInt64(searchattributes.PyckSortKeyKey)
	PyckTransactionID        = temporal.NewSearchAttributeKeyKeyword(searchattributes.PyckTransactionIDKey)
)

// SearchAttributes lists every search attribute pyck registers per namespace.
// Ten of them are Keyword type, and Temporal's SQL visibility (postgres12, which
// our deployment uses) has exactly Keyword01..10 per namespace. All Keyword slots
// are used, so no further Keyword attribute can be added, and an operator must
// not register a custom Keyword attribute in pyck namespaces.
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
	PyckTitle,
	PyckGroupTitle,
	PyckSortKey,
	PyckTransactionID,
}
