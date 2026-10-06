package workflow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/namespace/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"golang.org/x/sync/singleflight"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/memkv"
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
	DefaultTaskQueue = "default"

	// MemoEventID is the memo key under which the signal router records the ID
	// of the event that started a run through Signal-With-Start. The value is
	// the event ID as a string, encoded by the SDK's default data converter.
	MemoEventID = "pyck_event_id"

	// MaxWorkflowIDLength is Temporal's default limit.maxIDLength, in bytes.
	// Temporal enforces it when a workflow starts, so no longer ID can exist.
	MaxWorkflowIDLength = 1000
)

// ValidateWorkflowID rejects an empty, too long or NUL-containing workflow ID.
// Temporal retries its store's rejection of NUL until the 10 s RPC deadline.
func ValidateWorkflowID(workflowID string) error {
	if workflowID == "" || len(workflowID) > MaxWorkflowIDLength || strings.ContainsRune(workflowID, '\x00') {
		return ErrInvalidWorkflowID
	}
	return nil
}

// Client is a wrapper around the Temporal client providing workflow-related operations.
type Client struct {
	temporal  temporalclient.Client
	namespace string
	// remoteUICache memoizes Worker Deployment lookups for remoteUI resolution:
	// per-version metadata (immutable, no TTL) and the deployment-version listing
	// (short TTL). Lazy expiry only (no cleanup goroutine), so it is safe to
	// create one per cached client.
	remoteUICache *memkv.InMemoryKVStore
	// versionFlight dedupes concurrent cold lookups of one deployment version.
	versionFlight singleflight.Group
	// unstampedVersionCacheTTL is the negative-cache TTL of resolveVersionBundle.
	unstampedVersionCacheTTL time.Duration
}

// ClientOption customizes a Client at construction.
type ClientOption func(*Client)

// WithUnstampedVersionCacheTTL overrides DefaultUnstampedVersionCacheTTL; zero
// disables the negative cache.
func WithUnstampedVersionCacheTTL(ttl time.Duration) ClientOption {
	return func(c *Client) { c.unstampedVersionCacheTTL = ttl }
}

// StartWorkflowOptions represents options for starting a workflow.
// It is an alias for temporalclient.StartWorkflowOptions.
type StartWorkflowOptions = temporalclient.StartWorkflowOptions

// NewClient creates a new workflow Client with the given Temporal client.
func NewClient(namespace string, client temporalclient.Client, opts ...ClientOption) (*Client, error) {
	if namespace == "" {
		namespace = temporalclient.DefaultNamespace
	}

	c := &Client{
		temporal:                 client,
		namespace:                namespace,
		remoteUICache:            memkv.NewInMemoryKVStore(0),
		unstampedVersionCacheTTL: DefaultUnstampedVersionCacheTTL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// startOptions applies the defaults every start goes through: the task queue,
// a generated workflow ID, and the workflow name in the typed search attributes
// (merged with any the caller set), which signal routing relies on.
func (c *Client) startOptions(ctx context.Context, workflowTypeName string, options *temporalclient.StartWorkflowOptions) temporalclient.StartWorkflowOptions {
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

	return opts
}

// SignalWithStartWorkflow signals the workflow workflowID with signalName and
// signalArg, starting it first (with payload as its input) if it is not
// running. options are the start options as for StartWorkflowWithOptions; ID is
// set to workflowID. The caller chooses WorkflowIDConflictPolicy: Temporal
// rejects FAIL for signal-with-start, so use USE_EXISTING. WorkflowIDReusePolicy
// decides whether a finished run may be started again, and a refusal comes back
// as a WorkflowExecutionAlreadyStarted error, whatever
// WorkflowExecutionErrorWhenAlreadyStarted says. The SDK does not report whether
// the call started a run.
//
// This method is safe for concurrent use.
//
//nolint:ireturn // Returning WorkflowRun interface is required by Temporal SDK
func (c *Client) SignalWithStartWorkflow(ctx context.Context, workflowTypeName, workflowID, signalName string, signalArg, payload any, options *temporalclient.StartWorkflowOptions) (temporalclient.WorkflowRun, error) {
	if workflowID == "" {
		return nil, ErrInvalidWorkflowID
	}

	if signalName == "" {
		return nil, ErrInvalidSignalName
	}

	opts := c.startOptions(ctx, workflowTypeName, options)
	opts.ID = workflowID

	return c.temporal.SignalWithStartWorkflow(ctx, workflowID, signalName, signalArg, opts, workflowTypeName, payload)
}

// DescribeLatestRun describes the latest run of workflowID, running or closed.
// It fails with a NotFound error if the workflow ID has never been used.
func (c *Client) DescribeLatestRun(ctx context.Context, workflowID string) (*workflow.WorkflowExecutionInfo, error) {
	if workflowID == "" {
		return nil, ErrInvalidWorkflowID
	}

	resp, err := c.temporal.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		return nil, fmt.Errorf("failed to describe workflow: %w", err)
	}

	return resp.GetWorkflowExecutionInfo(), nil
}

// StartedByEvent reports whether the run described by info records eventID in
// its MemoEventID memo, that is, whether that event started it.
func StartedByEvent(info *workflow.WorkflowExecutionInfo, eventID string) bool {
	payload := info.GetMemo().GetFields()[MemoEventID]
	if payload == nil {
		return false
	}

	var recorded string

	if err := converter.GetDefaultDataConverter().FromPayload(payload, &recorded); err != nil {
		return false
	}

	return recorded == eventID
}

// SignalWorkflowByID signals the current run of workflowID, without knowing its
// run ID. It fails with a NotFound error if there is no such workflow or its
// current run has closed.
func (c *Client) SignalWorkflowByID(ctx context.Context, workflowID, signalName string, arg any) error {
	if workflowID == "" {
		return ErrInvalidWorkflowID
	}

	if signalName == "" {
		return ErrInvalidSignalName
	}

	return c.temporal.SignalWorkflow(ctx, workflowID, "", signalName, arg)
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
	opts := c.startOptions(ctx, workflowTypeName, options)

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
	if err := ValidateWorkflowID(workflowID); err != nil {
		return err
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
	if err := ValidateWorkflowID(workflowID); err != nil {
		return nil, err
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
	if err := ValidateWorkflowID(workflowID); err != nil {
		return nil, err
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
	if err := ValidateWorkflowID(workflowID); err != nil {
		return err
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
	if err := ValidateWorkflowID(workflowID); err != nil {
		return err
	}

	if runID == "" {
		return ErrInvalidWorkflowRunID
	}

	if signalName == "" {
		return ErrInvalidSignalName
	}

	return c.temporal.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
}

// CancelWorkflow requests graceful cancellation of a running workflow execution.
//
// The target workflow receives a cancellation signal on its context and can
// run cleanup (e.g. via workflow.NewDisconnectedContext) before terminating.
// For immediate, forceful termination without cleanup, use TerminateWorkflow.
//
// This method is safe for concurrent use.
func (c *Client) CancelWorkflow(ctx context.Context, workflowID, runID string) error {
	if err := ValidateWorkflowID(workflowID); err != nil {
		return err
	}

	if runID == "" {
		return ErrInvalidWorkflowRunID
	}

	return c.temporal.CancelWorkflow(ctx, workflowID, runID)
}

// UpdateWorkflow sends an update to a running workflow execution.
//
// The result is unmarshaled into the value pointed to by result. If the
// workflow is not running or the update fails, an error is returned.
//
// This method is safe for concurrent use.
func (c *Client) UpdateWorkflow(ctx context.Context, workflowID, runID, updateName string, arg, result any) error {
	if err := ValidateWorkflowID(workflowID); err != nil {
		return err
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

// GetWorkflowActions queries the available actions from a workflow execution.
// Returns which queries and updates are registered and whether each is
// currently enabled. This method is safe for concurrent use.
func (c *Client) GetWorkflowActions(ctx context.Context, workflowID, runID string) (*AvailableActions, error) {
	var result AvailableActions

	if err := c.QueryWorkflow(ctx, workflowID, runID, WorkflowQueryTypeGetAvailableActions.String(), nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
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
