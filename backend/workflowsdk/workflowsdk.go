package workflowsdk

import (
	"slices"
	"time"

	"go.temporal.io/sdk/temporal"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/workflow"
	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

var (
	PyckDataID           = workflow.PyckDataID
	PyckWorkflowAssignee = workflow.PyckWorkflowAssignee
	PyckWorkflowName     = workflow.PyckWorkflowName
	PyckTenantID         = workflow.PyckTenantID
	PyckDataType         = workflow.PyckDataType
	PyckService          = workflow.PyckService
	PyckGroupBy          = workflow.PyckGroupBy

	defaultWaitForCancellation     = true
	defaultLocalActivityTimeout    = 10 * time.Second
	defaultActivityTimeout         = 10 * time.Minute
	defaultWorkflowTimeout         = 24 * time.Hour
	defaultRetryInterval           = 1 * time.Second
	defaultRetryBackoffCoefficient = 2.0
	defaultRetryMaximumInterval    = 1 * time.Minute

	defaultActivityOptions = temporalworkflow.ActivityOptions{
		StartToCloseTimeout: defaultActivityTimeout,
		WaitForCancellation: defaultWaitForCancellation,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    defaultRetryInterval,
			BackoffCoefficient: defaultRetryBackoffCoefficient,
			MaximumInterval:    defaultRetryMaximumInterval,
		},
	}

	defaultLocalActivityOptions = temporalworkflow.LocalActivityOptions{
		StartToCloseTimeout: defaultLocalActivityTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    defaultRetryInterval,
			BackoffCoefficient: defaultRetryBackoffCoefficient,
			MaximumInterval:    defaultRetryMaximumInterval,
		},
	}

	defaultChildOptions = temporalworkflow.ChildWorkflowOptions{
		WorkflowExecutionTimeout: defaultWorkflowTimeout,
		WaitForCancellation:      defaultWaitForCancellation,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    defaultRetryInterval,
			BackoffCoefficient: defaultRetryBackoffCoefficient,
			MaximumInterval:    defaultRetryMaximumInterval,
		},
	}
)

type (
	SignalTopic = events.MutationEventWithReplyTopic
)

type UserDataInputGetter interface {
	GetUserDataInput(ctx temporalworkflow.Context) common_workflow.UserDataInput
}

type WorkflowAssigneeSetter interface {
	SetAssignee(ctx temporalworkflow.Context, assignee common_workflow.WorkflowAssignee)
}

type WorkflowAssigneeGetter interface {
	GetAssignee(ctx temporalworkflow.Context) common_workflow.WorkflowAssignee
}

type ConfigurationLoader interface {
	LoadConfiguration(ctx temporalworkflow.Context) error
}

func SetWorkflowAssignee(ctx temporalworkflow.Context, assignee common_workflow.WorkflowAssignee) error {
	var update temporal.SearchAttributeUpdate

	switch {
	case assignee == nil:
		fallthrough
	case *assignee == "":
		update = PyckWorkflowAssignee.ValueUnset()
	default:
		update = PyckWorkflowAssignee.ValueSet(*assignee)
	}

	if err := temporalworkflow.UpsertTypedSearchAttributes(ctx, update); err != nil {
		return err
	}

	return nil
}

func GetWorkflowAssignee(ctx temporalworkflow.Context) common_workflow.WorkflowAssignee {
	if assignee, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeyword(PyckWorkflowAssignee); ok {
		return common_workflow.WorkflowAssignee(&assignee)
	}

	return nil
}

// SetupDefaults sets up common query handlers and initializations for workflows.
// It sets up handlers for getting the workflow state and pending user data input,
// and initializes the workflow assignee from search attributes if applicable.
//
//nolint:ireturn // Returning workflow.Context is required by Temporal SDK
func SetupDefaults[T any](ctx temporalworkflow.Context, state *T) (temporalworkflow.Context, error) {
	logger := temporalworkflow.GetLogger(ctx)

	ctx = temporalworkflow.WithActivityOptions(ctx, defaultActivityOptions)
	ctx = temporalworkflow.WithLocalActivityOptions(ctx, defaultLocalActivityOptions)
	ctx = temporalworkflow.WithChildOptions(ctx, defaultChildOptions)

	logger.Debug("applied default activity, local activity, and child workflow options")

	if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetState.String(),
		func() (*T, error) {
			return state, nil
		},
	); err != nil {
		return nil, err
	}

	logger.Debug("registered default workflow state query handler")

	if userDataInputGetter, ok := any(state).(UserDataInputGetter); ok {
		if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetUserDataInput.String(),
			func() (common_workflow.UserDataInput, error) {
				return userDataInputGetter.GetUserDataInput(ctx), nil
			},
		); err != nil {
			return nil, err
		}

		if err := temporalworkflow.SetUpdateHandler(ctx, workflow.WorkflowQueryTypeAwaitUserDataInput.String(),
			func(ctx temporalworkflow.Context, inputTypes []string) (*common_workflow.UserDataInput, error) {
				input := userDataInputGetter.GetUserDataInput(ctx)
				activityIndex := input.ActivityIndex

				if err := temporalworkflow.Await(ctx, func() bool {
					input = userDataInputGetter.GetUserDataInput(ctx)

					if input.ActivityIndex <= activityIndex {
						return false
					}

					if len(inputTypes) == 0 {
						return true
					}

					if input.Type != nil {
						if slices.Contains(inputTypes, input.Type.ID) {
							return true
						}
					}

					activityIndex = input.ActivityIndex

					return false
				}); err != nil {
					return nil, err
				}

				return &input, nil
			},
		); err != nil {
			return nil, err
		}

		logger.Debug("registered default user data input query handlers")
	}

	if assigneeGetter, ok := any(state).(WorkflowAssigneeGetter); ok {
		if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetAssignee.String(),
			func() (common_workflow.WorkflowAssignee, error) {
				return assigneeGetter.GetAssignee(ctx), nil
			},
		); err != nil {
			return nil, err
		}

		logger.Debug("registered default workflow assignee getter query handler")
	}

	if assigneeSetter, ok := any(state).(WorkflowAssigneeSetter); ok {
		if assignee, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeyword(PyckWorkflowAssignee); ok {
			assigneeSetter.SetAssignee(ctx, common_workflow.WorkflowAssignee(&assignee))
			logger.Debug("initialized workflow assignee from search attributes", "assignee", assignee)
		}

		if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeSetAssignee.String(),
			func(assignee common_workflow.WorkflowAssignee) (any, error) {
				return nil, SetWorkflowAssignee(ctx, assignee)
			},
		); err != nil {
			return nil, err
		}

		logger.Debug("registered default workflow assignee query handler")
	}

	if configLoader, ok := any(state).(ConfigurationLoader); ok {
		if err := configLoader.LoadConfiguration(ctx); err != nil {
			return nil, err
		}

		logger.Debug("implicitly loaded workflow configuration")
	}

	return ctx, nil
}

func SetGroupBy(ctx temporalworkflow.Context, groupBy string) error {
	if err := temporalworkflow.UpsertTypedSearchAttributes(ctx, PyckGroupBy.ValueSet(groupBy)); err != nil {
		return err
	}

	return nil
}

func GetGroupBy(ctx temporalworkflow.Context) string {
	if groupBy, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeyword(PyckGroupBy); ok {
		return groupBy
	}

	return ""
}

// WorkflowMemo represents the standard memo fields for workflow display in UI.
// These fields are used to show workflow information in lists and dashboards.
type WorkflowMemo struct {
	Title    string         `json:"title"`    // Primary display text (e.g., item name, order reference)
	Subtitle string         `json:"subtitle"` // Secondary display text (e.g., order ID, additional context)
	Data     map[string]any `json:"data"`     // Additional custom fields
}

// SetWorkflowMemo sets the workflow memo for UI display purposes.
// The memo is stored with the workflow and can be queried without loading the full workflow history.
// This is useful for displaying workflow information in lists and dashboards.
//
// Example:
//
//	workflowsdk.SetWorkflowMemo(ctx, workflowsdk.WorkflowMemo{
//	    Title:    "SKU-123",
//	    Subtitle: "order-uuid",
//	    Data: map[string]any{
//	        "priority": "high",
//	        "customer": "Acme Inc",
//	    },
//	})
//
// Results in memo: {"title": "SKU-123", "subtitle": "order-uuid", "data": {"priority": "high", "customer": "Acme Inc"}}
func SetWorkflowMemo(ctx temporalworkflow.Context, memo WorkflowMemo) error {
	// TODO: use json tags instead of direct map construction
	return temporalworkflow.UpsertMemo(ctx, map[string]any{
		"title":    memo.Title,
		"subtitle": memo.Subtitle,
		"data":     memo.Data,
	})
}
