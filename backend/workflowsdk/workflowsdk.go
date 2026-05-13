package workflowsdk

import (
	"fmt"
	"slices"
	"time"

	"go.temporal.io/sdk/temporal"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/pyck-ai/pyck/backend/common/events"
	"github.com/pyck-ai/pyck/backend/common/workflow"
	common_workflow "github.com/pyck-ai/pyck/backend/common/workflow"
)

var (
	PyckDataID               = workflow.PyckDataID
	PyckWorkflowAssignee     = workflow.PyckWorkflowAssignee
	PyckWorkflowIsAssignable = workflow.PyckWorkflowIsAssignable
	PyckWorkflowName         = workflow.PyckWorkflowName
	PyckWorkflowTargets      = workflow.PyckWorkflowTargets
	PyckTenantID             = workflow.PyckTenantID
	PyckDataType             = workflow.PyckDataType
	PyckService              = workflow.PyckService
	PyckGroupBy              = workflow.PyckGroupBy
	PyckTitle                = workflow.PyckTitle
	PyckGroupTitle           = workflow.PyckGroupTitle
	PyckSortKey              = workflow.PyckSortKey

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

// WorkflowTargetsSetter is implemented by workflow state that wants the SDK to
// wire the SetTargets update handler. SetTargets is called both at workflow
// start (to seed state from the search attribute) and from the GraphQL update.
type WorkflowTargetsSetter interface {
	SetTargets(ctx temporalworkflow.Context, targets []common_workflow.WorkflowTarget)
}

// WorkflowTargetsGetter is implemented by workflow state that wants the SDK to
// wire the GetTargets query handler.
type WorkflowTargetsGetter interface {
	GetTargets(ctx temporalworkflow.Context) []common_workflow.WorkflowTarget
}

type WorkflowIsAssignableSetter interface {
	SetIsAssignable(ctx temporalworkflow.Context, isAssignable bool)
}

type WorkflowIsAssignableGetter interface {
	GetIsAssignable(ctx temporalworkflow.Context) bool
}

type ConfigurationLoader interface {
	LoadConfiguration(ctx temporalworkflow.Context) error
}

// AvailableActionsProvider is optionally implemented by workflow state to
// selectively enable or disable actions. The default list (all registered
// handlers, all enabled) is passed in; the implementation returns a
// modified copy. Workflows that do not implement this interface get the
// default as-is.
type AvailableActionsProvider interface {
	GetAvailableActions(ctx temporalworkflow.Context, defaults common_workflow.AvailableActions) common_workflow.AvailableActions
}

// ActionRegistry tracks per-workflow action overrides. Created by
// SetupDefaults and stored in the workflow context so that helper
// functions like DisableAction/EnableAction can modify it from
// anywhere in the workflow.
type ActionRegistry struct {
	disabled map[string]bool // action name -> true if disabled
}

type actionRegistryKey struct{}

// DisableAction marks an action as disabled for the current workflow.
// The change takes effect on the next GetAvailableActions query.
func DisableAction(ctx temporalworkflow.Context, name string) {
	if reg := getActionRegistry(ctx); reg != nil {
		reg.disabled[name] = true
	}
}

// EnableAction re-enables a previously disabled action.
func EnableAction(ctx temporalworkflow.Context, name string) {
	if reg := getActionRegistry(ctx); reg != nil {
		delete(reg.disabled, name)
	}
}

func getActionRegistry(ctx temporalworkflow.Context) *ActionRegistry {
	reg, _ := ctx.Value(actionRegistryKey{}).(*ActionRegistry)
	return reg
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
		return fmt.Errorf("upsert pyck_workflow_assignee search attribute: %w", err)
	}

	return nil
}

func GetWorkflowAssignee(ctx temporalworkflow.Context) common_workflow.WorkflowAssignee {
	if assignee, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeyword(PyckWorkflowAssignee); ok {
		return common_workflow.WorkflowAssignee(&assignee)
	}

	return nil
}

// SetWorkflowTargets writes the workflow's target surfaces into the
// pyck_workflow_targets KeywordList search attribute. Passing an empty or nil
// slice unsets the attribute so the workflow no longer matches any
// targets-based filter.
func SetWorkflowTargets(ctx temporalworkflow.Context, targets []common_workflow.WorkflowTarget) error {
	var update temporal.SearchAttributeUpdate

	if len(targets) == 0 {
		update = PyckWorkflowTargets.ValueUnset()
	} else {
		values := make([]string, 0, len(targets))
		for _, t := range targets {
			values = append(values, t.String())
		}
		update = PyckWorkflowTargets.ValueSet(values)
	}

	if err := temporalworkflow.UpsertTypedSearchAttributes(ctx, update); err != nil {
		return err
	}

	return nil
}

// GetWorkflowTargets reads the pyck_workflow_targets search attribute and
// converts it back to the typed enum. Unknown strings are silently dropped so
// that workflows running on an older binary cannot be poisoned by a newer
// target value written from a future deploy.
func GetWorkflowTargets(ctx temporalworkflow.Context) []common_workflow.WorkflowTarget {
	values, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeywordList(PyckWorkflowTargets)
	if !ok || len(values) == 0 {
		return nil
	}

	targets := make([]common_workflow.WorkflowTarget, 0, len(values))
	for _, v := range values {
		t, err := common_workflow.WorkflowTargetString(v)
		if err != nil {
			continue
		}
		targets = append(targets, t)
	}

	return targets
}

// SetWorkflowIsAssignable writes the is_assignable search attribute. Values
// are always persisted via ValueSet (including false) so the attribute remains
// queryable and never enters an unset state.
func SetWorkflowIsAssignable(ctx temporalworkflow.Context, isAssignable bool) error {
	if err := temporalworkflow.UpsertTypedSearchAttributes(ctx, PyckWorkflowIsAssignable.ValueSet(isAssignable)); err != nil {
		return fmt.Errorf("upsert pyck_workflow_is_assignable search attribute: %w", err)
	}

	return nil
}

// GetWorkflowIsAssignable reads the is_assignable search attribute. Workflows
// that have not yet written the attribute are treated as assignable.
func GetWorkflowIsAssignable(ctx temporalworkflow.Context) bool {
	if isAssignable, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetBool(PyckWorkflowIsAssignable); ok {
		return isAssignable
	}

	return true
}

// initializeIsAssignable syncs the setter's state with the stored search
// attribute, or persists the default value (true) when none is stored yet.
func initializeIsAssignable(ctx temporalworkflow.Context, setter WorkflowIsAssignableSetter) error {
	logger := temporalworkflow.GetLogger(ctx)

	if isAssignable, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetBool(PyckWorkflowIsAssignable); ok {
		setter.SetIsAssignable(ctx, isAssignable)
		logger.Debug("initialized workflow is_assignable from search attributes", "is_assignable", isAssignable)
		return nil
	}

	if err := SetWorkflowIsAssignable(ctx, true); err != nil {
		return err
	}
	setter.SetIsAssignable(ctx, true)
	logger.Debug("initialized workflow is_assignable default", "is_assignable", true)

	return nil
}

// SetupDefaults registers Temporal query and update handlers for the
// interface-optional extensions a workflow state struct may implement.
// It is expected to be called once at the top of the workflow function,
// immediately after allocating the per-execution state.
//
// Handlers registered unconditionally:
//   - GetState (query): returns the state pointer.
//
// Handlers registered when the state implements the matching interface:
//   - UserDataInputGetter   — GetUserDataInput (query),
//     AwaitUserDataInput (update: blocks on new input).
//   - WorkflowAssigneeGetter — GetAssignee (query).
//   - WorkflowAssigneeSetter — SetAssignee (update: writes the
//     pyck_workflow_assignee search attribute and syncs state).
//   - WorkflowIsAssignableGetter — GetIsAssignable (query).
//   - WorkflowIsAssignableSetter — SetIsAssignable (update: writes the
//     pyck_workflow_is_assignable search attribute and syncs state).
//     On first call, initialises the attribute to true when absent so
//     the workflow shows up in the default assignable filter.
//   - WorkflowTargetsGetter  — GetTargets (query).
//   - WorkflowTargetsSetter  — SetTargets (update: writes the
//     pyck_workflow_targets search attribute and syncs state).
//   - ConfigurationLoader    — invokes LoadConfiguration synchronously.
//
// Returns a derived workflow.Context with default activity, local-activity,
// and child-workflow options applied.
//
//nolint:ireturn // Returning workflow.Context is required by Temporal SDK
func SetupDefaults[T any](ctx temporalworkflow.Context, state *T) (temporalworkflow.Context, error) {
	logger := temporalworkflow.GetLogger(ctx)

	ctx = temporalworkflow.WithActivityOptions(ctx, defaultActivityOptions)
	ctx = temporalworkflow.WithLocalActivityOptions(ctx, defaultLocalActivityOptions)
	ctx = temporalworkflow.WithChildOptions(ctx, defaultChildOptions)

	logger.Debug("applied default activity, local activity, and child workflow options")

	// Track registered handlers so GetAvailableActions can report them.
	var registeredQueries []string
	var registeredUpdates []string

	if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetState.String(),
		func() (*T, error) {
			return state, nil
		},
	); err != nil {
		return nil, err
	}
	registeredQueries = append(registeredQueries, workflow.WorkflowQueryTypeGetState.String())

	logger.Debug("registered default workflow state query handler")

	if userDataInputGetter, ok := any(state).(UserDataInputGetter); ok {
		if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetUserDataInput.String(),
			func() (common_workflow.UserDataInput, error) {
				return userDataInputGetter.GetUserDataInput(ctx), nil
			},
		); err != nil {
			return nil, err
		}
		registeredQueries = append(registeredQueries, workflow.WorkflowQueryTypeGetUserDataInput.String())

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
		registeredUpdates = append(registeredUpdates, workflow.WorkflowQueryTypeAwaitUserDataInput.String())

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
		registeredQueries = append(registeredQueries, workflow.WorkflowQueryTypeGetAssignee.String())

		logger.Debug("registered default workflow assignee getter query handler")
	}

	if assigneeSetter, ok := any(state).(WorkflowAssigneeSetter); ok {
		if assignee, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeyword(PyckWorkflowAssignee); ok {
			assigneeSetter.SetAssignee(ctx, common_workflow.WorkflowAssignee(&assignee))
			logger.Debug("initialized workflow assignee from search attributes", "assignee", assignee)
		}

		// Must be SetUpdateHandler (not SetQueryHandler): the body calls
		// UpsertTypedSearchAttributes, which panics from a query context —
		// queries are read-only. The input type must be the
		// WorkflowAssigneeUpdaterInput wrapper because the resolver marshals
		// {"assignee": …} (the JSON-schema-carrying shape), not a bare
		// WorkflowAssignee. Breaking either invariant silently breaks the
		// setWorkflowAssignee mutation at the first live call.
		if err := temporalworkflow.SetUpdateHandler(ctx, workflow.WorkflowQueryTypeSetAssignee.String(),
			func(ctx temporalworkflow.Context, input common_workflow.WorkflowAssigneeUpdaterInput) (any, error) {
				if err := SetWorkflowAssignee(ctx, input.Assignee); err != nil {
					return nil, err
				}
				assigneeSetter.SetAssignee(ctx, input.Assignee)
				return struct{}{}, nil
			},
		); err != nil {
			return nil, err
		}
		registeredQueries = append(registeredQueries, workflow.WorkflowQueryTypeSetAssignee.String())

		logger.Debug("registered default workflow assignee update handler")
	}

	if isAssignableGetter, ok := any(state).(WorkflowIsAssignableGetter); ok {
		if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetIsAssignable.String(),
			func() (bool, error) {
				return isAssignableGetter.GetIsAssignable(ctx), nil
			},
		); err != nil {
			return nil, err
		}

		logger.Debug("registered default workflow is_assignable getter query handler")
	}

	if isAssignableSetter, ok := any(state).(WorkflowIsAssignableSetter); ok {
		if err := initializeIsAssignable(ctx, isAssignableSetter); err != nil {
			return nil, err
		}

		// Must be SetUpdateHandler (not SetQueryHandler): the body calls
		// UpsertTypedSearchAttributes, which panics from a query context —
		// queries are read-only. The input type must be the
		// WorkflowIsAssignableUpdaterInput wrapper because the resolver
		// marshals {"is_assignable": bool} (the JSON-schema-carrying shape),
		// not a bare bool. Breaking either invariant silently breaks the
		// setWorkflowIsAssignable mutation at the first live call.
		if err := temporalworkflow.SetUpdateHandler(ctx, workflow.WorkflowQueryTypeSetIsAssignable.String(),
			func(ctx temporalworkflow.Context, input common_workflow.WorkflowIsAssignableUpdaterInput) (any, error) {
				if err := SetWorkflowIsAssignable(ctx, input.IsAssignable); err != nil {
					return nil, err
				}
				isAssignableSetter.SetIsAssignable(ctx, input.IsAssignable)
				return struct{}{}, nil
			},
		); err != nil {
			return nil, err
		}

		logger.Debug("registered default workflow is_assignable update handler")
	}

	if targetsGetter, ok := any(state).(WorkflowTargetsGetter); ok {
		if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetTargets.String(),
			func() ([]common_workflow.WorkflowTarget, error) {
				return targetsGetter.GetTargets(ctx), nil
			},
		); err != nil {
			return nil, err
		}
		registeredQueries = append(registeredQueries, workflow.WorkflowQueryTypeGetTargets.String())

		logger.Debug("registered default workflow targets getter query handler")
	}

	if targetsSetter, ok := any(state).(WorkflowTargetsSetter); ok {
		if existing := GetWorkflowTargets(ctx); len(existing) > 0 {
			targetsSetter.SetTargets(ctx, existing)
			logger.Debug("initialized workflow targets from search attributes", "targets", existing)
		}

		if err := temporalworkflow.SetUpdateHandler(ctx, workflow.WorkflowQueryTypeSetTargets.String(),
			func(ctx temporalworkflow.Context, value common_workflow.WorkflowTargetsUpdaterInput) (any, error) {
				if err := SetWorkflowTargets(ctx, value.Targets); err != nil {
					return nil, err
				}
				targetsSetter.SetTargets(ctx, value.Targets)
				return value.Targets, nil
			},
		); err != nil {
			return nil, err
		}
		registeredUpdates = append(registeredUpdates, workflow.WorkflowQueryTypeSetTargets.String())

		logger.Debug("registered default workflow targets setter update handler")
	}

	if configLoader, ok := any(state).(ConfigurationLoader); ok {
		if err := configLoader.LoadConfiguration(ctx); err != nil {
			return nil, err
		}

		logger.Debug("implicitly loaded workflow configuration")
	}

	// ActionRegistry: stores per-action overrides from DisableAction/EnableAction.
	registry := &ActionRegistry{disabled: make(map[string]bool)}
	ctx = temporalworkflow.WithValue(ctx, actionRegistryKey{}, registry)

	// GetAvailableActions is always registered. The default lists all
	// registered handlers with Enabled=true. Three layers of customization
	// are applied in order:
	//   1. Default: all registered handlers, all Enabled=true
	//   2. Registry overrides: DisableAction/EnableAction toggle individual actions
	//   3. Provider override: AvailableActionsProvider for full custom logic
	actionsProvider, _ := any(state).(AvailableActionsProvider)
	defaultActions := buildDefaultActions(registeredQueries, registeredUpdates)

	if err := temporalworkflow.SetQueryHandler(ctx, workflow.WorkflowQueryTypeGetAvailableActions.String(),
		func() (common_workflow.AvailableActions, error) {
			actions := applyRegistryOverrides(defaultActions, registry)
			if actionsProvider != nil {
				actions = actionsProvider.GetAvailableActions(ctx, actions)
			}
			return actions, nil
		},
	); err != nil {
		return nil, err
	}

	logger.Debug("registered available actions query handler")

	return ctx, nil
}

// buildDefaultActions creates an AvailableActions with all registered
// handlers listed and Enabled set to true.
func buildDefaultActions(queries, updates []string) common_workflow.AvailableActions {
	actions := common_workflow.AvailableActions{
		Queries: make([]common_workflow.ActionDefinition, len(queries)),
		Updates: make([]common_workflow.ActionDefinition, len(updates)),
	}
	for i, name := range queries {
		actions.Queries[i] = common_workflow.ActionDefinition{Name: name, Enabled: true}
	}
	for i, name := range updates {
		actions.Updates[i] = common_workflow.ActionDefinition{Name: name, Enabled: true}
	}
	return actions
}

// applyRegistryOverrides returns a copy of defaults with Enabled=false
// for any action that has been disabled via DisableAction.
func applyRegistryOverrides(defaults common_workflow.AvailableActions, registry *ActionRegistry) common_workflow.AvailableActions {
	if len(registry.disabled) == 0 {
		return defaults
	}
	result := common_workflow.AvailableActions{
		Queries: make([]common_workflow.ActionDefinition, len(defaults.Queries)),
		Updates: make([]common_workflow.ActionDefinition, len(defaults.Updates)),
	}
	copy(result.Queries, defaults.Queries)
	copy(result.Updates, defaults.Updates)
	for i := range result.Queries {
		if registry.disabled[result.Queries[i].Name] {
			result.Queries[i].Enabled = false
		}
	}
	for i := range result.Updates {
		if registry.disabled[result.Updates[i].Name] {
			result.Updates[i].Enabled = false
		}
	}
	return result
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

// SetWorkflowTitle writes the pyck_title search attribute used as the
// per-workflow display label in the UI. Passing an empty string unsets the
// attribute so the frontend falls back to the workflow type name.
func SetWorkflowTitle(ctx temporalworkflow.Context, title string) error {
	var update temporal.SearchAttributeUpdate
	if title == "" {
		update = PyckTitle.ValueUnset()
	} else {
		update = PyckTitle.ValueSet(title)
	}

	if err := temporalworkflow.UpsertTypedSearchAttributes(ctx, update); err != nil {
		return fmt.Errorf("upsert pyck_title search attribute: %w", err)
	}

	return nil
}

// GetWorkflowTitle reads the pyck_title search attribute.
func GetWorkflowTitle(ctx temporalworkflow.Context) string {
	if title, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeyword(PyckTitle); ok {
		return title
	}

	return ""
}

// SetGroupTitle writes the pyck_group_title search attribute used as the
// human-readable label for the group header in the UI. It pairs with
// pyck_group_by (the technical join key) — set both together. Passing an empty
// string unsets the attribute so the frontend falls back to "No Group".
func SetGroupTitle(ctx temporalworkflow.Context, groupTitle string) error {
	var update temporal.SearchAttributeUpdate
	if groupTitle == "" {
		update = PyckGroupTitle.ValueUnset()
	} else {
		update = PyckGroupTitle.ValueSet(groupTitle)
	}

	if err := temporalworkflow.UpsertTypedSearchAttributes(ctx, update); err != nil {
		return fmt.Errorf("upsert pyck_group_title search attribute: %w", err)
	}

	return nil
}

// GetGroupTitle reads the pyck_group_title search attribute.
func GetGroupTitle(ctx temporalworkflow.Context) string {
	if groupTitle, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetKeyword(PyckGroupTitle); ok {
		return groupTitle
	}

	return ""
}

// SetSortKey writes the pyck_sort_key search attribute used by the UI to
// impose a stable display order on workflow executions. Pass nil to unset the
// attribute so the frontend falls back to the default ordering (start time).
func SetSortKey(ctx temporalworkflow.Context, sortKey *int64) error {
	var update temporal.SearchAttributeUpdate
	if sortKey == nil {
		update = PyckSortKey.ValueUnset()
	} else {
		update = PyckSortKey.ValueSet(*sortKey)
	}

	if err := temporalworkflow.UpsertTypedSearchAttributes(ctx, update); err != nil {
		return fmt.Errorf("upsert pyck_sort_key search attribute: %w", err)
	}

	return nil
}

// GetSortKey reads the pyck_sort_key search attribute. Returns nil when the
// attribute has never been written for this workflow.
func GetSortKey(ctx temporalworkflow.Context) *int64 {
	if sortKey, ok := temporalworkflow.GetTypedSearchAttributes(ctx).GetInt64(PyckSortKey); ok {
		return &sortKey
	}

	return nil
}
