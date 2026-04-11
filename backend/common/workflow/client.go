package workflow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/namespace/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/pyck-ai/pyck/backend/common/log"
)

var (
	ErrInvalidWorkflowID    = errors.New("invalid WorkflowID")
	ErrInvalidWorkflowRunID = errors.New("invalid WorkflowRunID")
	ErrInvalidSignalName    = errors.New("invalid SignalName")
	ErrInvalidUpdateName    = errors.New("invalid UpdateName")
	ErrInvalidQueryName     = errors.New("invalid QueryName")
	ErrPageSizeOverflow     = errors.New("pageSize exceeds maximum int32 value")
)

const (
	DefaultTaskQueue      = "default"
	ClientCreationTimeout = 30 * time.Second
)

// Client is a wrapper around the Temporal client providing workflow-related operations.
type Client struct {
	temporal  temporalclient.Client
	namespace string
}

// StartWorkflowOptions represents options for starting a workflow.
// It is an alias for temporalclient.StartWorkflowOptions.
type StartWorkflowOptions = temporalclient.StartWorkflowOptions

// NewClient creates a new workflow Client with the given Temporal client.
func NewClient(namespace string, client temporalclient.Client) (*Client, error) {
	if namespace == "" {
		namespace = temporalclient.DefaultNamespace
	}

	return &Client{
		temporal:  client,
		namespace: namespace,
	}, nil
}

// StartWorkflowWithOptions starts a workflow execution.
//
// If options are nil, defaults are used. If unspecified, the WorkflowID is
// auto-generated and the TaskQueue defaults to DefaultTaskQueue. The workflow
// type name is automatically set in search attributes.
//
// This method is safe for concurrent use.
//
// Example:
//
//	run, err := client.StartWorkflowWithOptions(ctx, "MyType", payload, nil)
//	if err != nil {
//		// handle error
//	}
//
//nolint:ireturn // Returning WorkflowRun interface is required by Temporal SDK
func (c *Client) StartWorkflowWithOptions(ctx context.Context, workflowTypeName string, payload any, options *temporalclient.StartWorkflowOptions) (temporalclient.WorkflowRun, error) {
	var opts temporalclient.StartWorkflowOptions

	if options != nil {
		opts = *options
	}

	if opts.TaskQueue == "" {
		opts.TaskQueue = DefaultTaskQueue
	}

	// Ensure a unique workflow ID if not provided
	if opts.ID == "" {
		opts.ID = workflowTypeName + "_" + uuid.New().String()
	}

	// Ensure we can find the workflow again using TYPED SEARCH ATTRIBUTES
	// Always set PyckWorkflowName as it's required for signal routing
	// If the caller provided other search attributes, merge them
	if opts.TypedSearchAttributes.Size() > 0 {
		// Check if workflow name was already set and warn if so
		if existingName, ok := opts.TypedSearchAttributes.GetKeyword(PyckWorkflowName); ok {
			log.ForContext(ctx).Warn().
				Str("workflow", workflowTypeName).
				Str("existingName", existingName).
				Msg("overriding PyckWorkflowName that was already set in search attributes")
		}

		// Create new search attributes merging existing ones with workflow name
		// The workflow name is set last to ensure it takes precedence
		opts.TypedSearchAttributes = temporal.NewSearchAttributes(
			opts.TypedSearchAttributes.Copy(),
			PyckWorkflowName.ValueSet(workflowTypeName),
		)
	} else {
		// No existing attributes, just set the workflow name
		opts.TypedSearchAttributes = temporal.NewSearchAttributes(
			PyckWorkflowName.ValueSet(workflowTypeName),
		)
	}

	workflow, err := c.temporal.ExecuteWorkflow(ctx, opts, workflowTypeName, payload)
	if err != nil {
		return nil, err
	}

	log.ForContext(ctx).Debug().
		Str("workflow", workflowTypeName).
		Str("workflowID", workflow.GetID()).
		Str("runID", workflow.GetRunID()).
		Msg("started workflow")

	return workflow, nil
}

// GetWorkflowResult retrieves the result of a completed workflow execution.
//
// The result is unmarshaled into the value pointed to by valuePtr. If the
// workflow is not yet completed, an error is returned.
//
// This method is safe for concurrent use.
func (c *Client) GetWorkflowResult(ctx context.Context, workflowID string, runID string, valuePtr any) error {
	if workflowID == "" {
		return ErrInvalidWorkflowID
	}

	if runID == "" {
		return ErrInvalidWorkflowRunID
	}

	return c.temporal.GetWorkflow(ctx, workflowID, runID).Get(ctx, valuePtr)
}

// GetWorkflowExecutionInfo retrieves information about a workflow execution.
//
// This method is safe for concurrent use.
func (c *Client) GetWorkflowExecutionInfo(ctx context.Context, workflowID, runID string) (*workflow.WorkflowExecutionInfo, error) {
	if workflowID == "" {
		return nil, ErrInvalidWorkflowID
	}

	if runID == "" {
		return nil, ErrInvalidWorkflowRunID
	}

	resp, err := c.temporal.DescribeWorkflowExecution(ctx, workflowID, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to describe workflow: %w", err)
	}

	return resp.GetWorkflowExecutionInfo(), nil
}

// ListWorkflowsPage lists a single page of workflow executions based on the provided query.
// Returns the executions and the next page token for pagination.
// If nextPageToken is nil or empty, returns the first page.
//
// This method is safe for concurrent use.
func (c *Client) ListWorkflowsPage(ctx context.Context, query string, pageSize int, nextPageToken []byte) ([]*workflow.WorkflowExecutionInfo, []byte, error) {
	if pageSize > math.MaxInt32 {
		return nil, nil, ErrPageSizeOverflow
	}

	resp, err := c.temporal.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Query:         query,
		Namespace:     c.namespace,
		PageSize:      int32(pageSize), //nolint:gosec // overflow checked above
		NextPageToken: nextPageToken,
	})
	if err != nil {
		return nil, nil, err
	}

	return resp.GetExecutions(), resp.GetNextPageToken(), nil
}

// ListWorkflows lists all workflow executions based on the provided query.
// It iterates through all pages and returns a combined list.
//
// This method is safe for concurrent use.
func (c *Client) ListWorkflows(ctx context.Context, query string) ([]*workflow.WorkflowExecutionInfo, error) {
	var (
		workflows []*workflow.WorkflowExecutionInfo
		pageToken []byte
	)

	for {
		resp, err := c.temporal.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Query:         query,
			Namespace:     c.namespace,
			NextPageToken: pageToken,
		})
		if err != nil {
			return nil, err
		}

		workflows = append(workflows, resp.GetExecutions()...)

		pageToken = resp.GetNextPageToken()
		if len(pageToken) == 0 {
			break
		}
	}

	return workflows, nil
}

func (c *Client) GetWorkflowHistory(ctx context.Context, workflowID, runID string) ([]*historypb.HistoryEvent, error) {
	if workflowID == "" {
		return nil, ErrInvalidWorkflowID
	}

	if runID == "" {
		return nil, ErrInvalidWorkflowRunID
	}

	var history []*historypb.HistoryEvent

	iter := c.temporal.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("failed to get next history event: %w", err)
		}
		history = append(history, event)
	}

	return history, nil
}

// QueryWorkflow sends a query to a running workflow execution.
//
// The result is unmarshaled into the value pointed to by resultPtr. If the
// workflow is not running or the query fails, an error is returned.
//
// This method is safe for concurrent use.
func (c *Client) QueryWorkflow(ctx context.Context, workflowID, runID, queryName string, arg, result any) error {
	if workflowID == "" {
		return ErrInvalidWorkflowID
	}

	if runID == "" {
		return ErrInvalidWorkflowRunID
	}

	if queryName == "" {
		return ErrInvalidQueryName
	}

	response, err := c.temporal.QueryWorkflow(ctx, workflowID, runID, queryName, arg)
	if err != nil {
		return fmt.Errorf("failed to query workflow: %w", err)
	}

	if result != nil {
		if err := response.Get(&result); err != nil {
			return fmt.Errorf("failed to get query result: %w", err)
		}
	}

	return nil
}

// SignalWorkflow sends a signal to a running workflow execution.
//
// This method is safe for concurrent use.
func (c *Client) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error {
	if workflowID == "" {
		return ErrInvalidWorkflowID
	}

	if runID == "" {
		return ErrInvalidWorkflowRunID
	}

	if signalName == "" {
		return ErrInvalidSignalName
	}

	return c.temporal.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
}

// UpdateWorkflow sends an update to a running workflow execution.
//
// The result is unmarshaled into the value pointed to by result. If the
// workflow is not running or the update fails, an error is returned.
//
// This method is safe for concurrent use.
func (c *Client) UpdateWorkflow(ctx context.Context, workflowID, runID, updateName string, arg, result any) error {
	if workflowID == "" {
		return ErrInvalidWorkflowID
	}

	if runID == "" {
		return ErrInvalidWorkflowRunID
	}

	if updateName == "" {
		return ErrInvalidUpdateName
	}

	updateHandle, err := c.temporal.UpdateWorkflow(ctx, temporalclient.UpdateWorkflowOptions{
		WorkflowID:   workflowID,
		RunID:        runID,
		UpdateName:   updateName,
		Args:         []any{arg},
		WaitForStage: temporalclient.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return fmt.Errorf("failed to send update: %w", err)
	}

	if result != nil {
		if err := updateHandle.Get(ctx, result); err != nil {
			return fmt.Errorf("failed to get update result: %w", err)
		}
	}

	return nil
}

// GetCurrentUserDataInput queries the current user data input from a workflow
// execution.
//
// The result is returned as a UserDataInput pointer. If the workflow is not
// running or the query fails, an error is returned.
//
// This method is safe for concurrent use.
func (c *Client) GetCurrentUserDataInput(ctx context.Context, workflowID, runID string) (*UserDataInput, error) {
	var result UserDataInput

	if err := c.QueryWorkflow(ctx, workflowID, runID, WorkflowQueryTypeGetUserDataInput.String(), nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// AwaitNextUserDataInput sends an update to a workflow execution to await the
// next user data input of specified types.
//
// The result is returned as a UserDataInput pointer. If the workflow is not
// running or the update fails, an error is returned.
//
// This method is safe for concurrent use.
func (c *Client) AwaitNextUserDataInput(ctx context.Context, workflowID, runID string, waitForTypes []string) (*UserDataInput, error) {
	resp, err := c.temporal.UpdateWorkflow(ctx, temporalclient.UpdateWorkflowOptions{
		WorkflowID:   workflowID,
		RunID:        runID,
		UpdateName:   WorkflowQueryTypeAwaitUserDataInput.String(),
		Args:         []any{waitForTypes},
		WaitForStage: temporalclient.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send await next user data input update: %w", err)
	}

	var input UserDataInput
	if err := resp.Get(ctx, &input); err != nil {
		return nil, fmt.Errorf("failed to get next user data input: %w", err)
	}

	return &input, nil
}

// Close closes the workflow client and releases any resources.
func (c *Client) Close() {
	if c.temporal != nil {
		c.temporal.Close()
	}
}

func (c *Client) GetNamespaces(ctx context.Context) ([]*namespace.NamespaceInfo, error) {
	var (
		pageToken  []byte
		namespaces []*namespace.NamespaceInfo
	)

	for {
		resp, err := c.temporal.WorkflowService().ListNamespaces(ctx, &workflowservice.ListNamespacesRequest{
			NextPageToken: pageToken,
		})
		if err != nil {
			return nil, err
		}

		for _, ns := range resp.GetNamespaces() {
			namespaces = append(namespaces, ns.GetNamespaceInfo())
		}

		pageToken = resp.GetNextPageToken()
		if len(pageToken) == 0 {
			break
		}
	}

	return namespaces, nil
}

func (c *Client) GetNamespaceByID(ctx context.Context, id string) (*namespace.NamespaceInfo, error) {
	var pageToken []byte

	for {
		resp, err := c.temporal.WorkflowService().ListNamespaces(ctx, &workflowservice.ListNamespacesRequest{
			NextPageToken: pageToken,
		})
		if err != nil {
			return nil, err
		}

		for _, ns := range resp.GetNamespaces() {
			if ns.GetNamespaceInfo().GetId() == id {
				return ns.GetNamespaceInfo(), nil
			}
		}

		pageToken = resp.GetNextPageToken()
		if len(pageToken) == 0 {
			break
		}
	}

	return nil, ErrNamespaceNotFound
}

var ErrNamespaceNotFound = fmt.Errorf("namespace not found")
