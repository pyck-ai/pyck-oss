package resolvers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"go.temporal.io/api/serviceerror"
	"golang.org/x/sync/errgroup"

	"github.com/pyck-ai/pyck/backend/common/request"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"
	"github.com/pyck-ai/pyck/backend/workflow/model"
)

const (
	// defaultHistoryLimit is the default number of executions to fetch history for.
	defaultHistoryLimit = 10
	// maxHistoryLimit is the maximum number of executions to fetch history for.
	maxHistoryLimit = 100
	// defaultPageSize is the default page size for workflow executions.
	defaultPageSize = 100
	// maxPageSize is the maximum allowed page size for workflow executions.
	maxPageSize = 1000
	// assignableOffsetPrefix is used to distinguish offset-based cursors from Temporal page tokens.
	assignableOffsetPrefix = "assignable_offset:"
)

// QuotedValues converts a []string into a comma-separated list of quoted strings.
func QuotedValues(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf(`%q`, v)
	}
	return strings.Join(quoted, ", ")
}

// FormatPredicate formats a single Temporal query predicate and returns it as a string.
// Handles single-value (*string), IN/NOT IN ([]string), and comparison operations.
// Returns an empty string if the value is nil, an empty string pointer, or an empty slice.
func FormatPredicate(temporalField string, value any, operator string) string {
	if value == nil {
		return ""
	}

	v := reflect.ValueOf(value)

	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() && v.Elem().Kind() == reflect.String && v.Elem().String() != "" {
			return fmt.Sprintf("%s %s %q", temporalField, operator, v.Elem().String())
		}
	case reflect.Slice:
		if v.Len() > 0 {
			strSlice := make([]string, v.Len())
			for i := range v.Len() {
				strSlice[i] = v.Index(i).String()
			}

			return fmt.Sprintf("%s %s (%s)", temporalField, operator, QuotedValues(strSlice))
		}
	}

	return ""
}

// sortExecutions sorts workflow executions based on the provided order.
func sortExecutions(executions []*model.WorkflowExecutionInfo, orderBy *model.WorkflowExecutionOrder) {
	if orderBy == nil {
		// Default: sort by start time descending
		sort.Slice(executions, func(i, j int) bool {
			return executions[i].StartTime > executions[j].StartTime
		})
		return
	}

	ascending := orderBy.Direction == model.WorkflowExecutionOrderDirectionAsc

	sort.Slice(executions, func(i, j int) bool {
		var less bool

		switch orderBy.Field {
		case model.WorkflowExecutionOrderFieldStartTime:
			less = executions[i].StartTime < executions[j].StartTime
		case model.WorkflowExecutionOrderFieldCloseTime:
			// Handle nil close times (running workflows)
			iClose := ""
			jClose := ""
			if executions[i].CloseTime != nil {
				iClose = *executions[i].CloseTime
			}
			if executions[j].CloseTime != nil {
				jClose = *executions[j].CloseTime
			}
			less = iClose < jClose
		case model.WorkflowExecutionOrderFieldWorkflowID:
			less = executions[i].Execution.WorkflowID < executions[j].Execution.WorkflowID
		case model.WorkflowExecutionOrderFieldStatus:
			less = executions[i].Status < executions[j].Status
		default:
			less = executions[i].StartTime < executions[j].StartTime
		}

		if ascending {
			return less
		}
		return !less
	})
}

// predicateDef pairs a struct field suffix with its Temporal query operator.
type predicateDef struct {
	suffix string // Go struct field suffix (e.g. "Neq", "In", "HasPrefix")
	op     string // Temporal query operator (e.g. "!=", "IN", "STARTS_WITH")
}

// Predicate definitions for each field type.
// Each entry maps a WorkflowExecutionsWhereInput field suffix to a Temporal operator.
// EqualFold/ContainsFold map to "="/"CONTAINS" because Temporal has no case-insensitive operators.
var (
	stringPredicates = []predicateDef{
		{"", "="},
		{"Neq", "!="},
		{"In", "IN"},
		{"NotIn", "NOT IN"},
		{"Contains", "CONTAINS"},
		{"HasPrefix", "STARTS_WITH"},
		{"HasSuffix", "ENDS_WITH"},
		{"EqualFold", "="},
		{"ContainsFold", "CONTAINS"},
	}
	stringPredicatesNoFold = stringPredicates[:7]
	timePredicates         = []predicateDef{
		{"", "="},
		{"Neq", "!="},
		{"In", "IN"},
		{"NotIn", "NOT IN"},
		{"Gt", ">"},
		{"Gte", ">="},
		{"Lt", "<"},
		{"Lte", "<="},
	}
)

// fieldMapping describes how a Temporal visibility field maps to the where input struct.
// The inputPrefix + each predicateDef.suffix is used to look up fields via reflection.
type fieldMapping struct {
	field             string         // Temporal visibility field name (e.g. "WorkflowType")
	inputPrefix       string         // Where input field prefix (e.g. "TypeName")
	predicates        []predicateDef // Suffix/operator pairs to resolve via reflection
	nullIncludesEmpty bool           // IS NULL also checks for empty string
	isNil             *bool          // Pointer to the IsNil field on the where input
	notNil            *bool          // Pointer to the NotNil field on the where input
}

// resolvePredicates uses reflection to look up where input fields by name convention
// (inputPrefix + suffix) and returns the non-zero values paired with their operators.
func (m fieldMapping) resolvePredicates(whereVal reflect.Value, whereType reflect.Type) []string {
	var result []string

	for _, pd := range m.predicates {
		targetName := m.inputPrefix + pd.suffix

		// Case-insensitive lookup handles generated name inconsistencies
		// (e.g. "WorkflowIdneq" vs "WorkflowIDNeq").
		for j := range whereType.NumField() {
			if strings.EqualFold(whereType.Field(j).Name, targetName) {
				fieldVal := whereVal.Field(j)
				if fieldVal.IsValid() && !fieldVal.IsZero() {
					if s := FormatPredicate(m.field, fieldVal.Interface(), pd.op); s != "" {
						result = append(result, s)
					}
				}

				break
			}
		}
	}

	return result
}

// nullPredicates returns IS NULL / IS NOT NULL clauses for this field.
func (m fieldMapping) nullPredicates() []string {
	var result []string

	if m.isNil != nil && *m.isNil {
		if m.nullIncludesEmpty {
			result = append(result, fmt.Sprintf(`(%s IS NULL OR %s = "")`, m.field, m.field))
		} else {
			result = append(result, m.field+" IS NULL")
		}
	}

	if m.notNil != nil && *m.notNil {
		if m.nullIncludesEmpty {
			result = append(result, fmt.Sprintf(`%s IS NOT NULL AND %s != ""`, m.field, m.field))
		} else {
			result = append(result, m.field+" IS NOT NULL")
		}
	}

	return result
}

// buildFieldPredicates builds the AND-joined field-level predicates for a single where input node.
// Returns an empty string if no field predicates are set.
func buildFieldPredicates(where *model.WorkflowExecutionsWhereInput) string {
	if where == nil {
		return ""
	}

	whereVal := reflect.ValueOf(where).Elem()
	whereType := whereVal.Type()

	mappings := []fieldMapping{
		{field: "WorkflowType", inputPrefix: "TypeName", predicates: stringPredicates},
		{field: "pyck_workflow_name", inputPrefix: "WorkflowName", predicates: stringPredicates},
		{field: "pyck_workflow_assignee", inputPrefix: "Assignee", predicates: stringPredicates, isNil: where.AssigneeIsNil, notNil: where.AssigneeNotNil, nullIncludesEmpty: true},
		{field: "pyck_group_by", inputPrefix: "GroupBy", predicates: stringPredicates, isNil: where.GroupByIsNil, notNil: where.GroupByNotNil, nullIncludesEmpty: true},
		{field: "WorkflowId", inputPrefix: "WorkflowID", predicates: stringPredicates},
		{field: "RunId", inputPrefix: "RunID", predicates: stringPredicates},
		{field: "ExecutionStatus", inputPrefix: "Status", predicates: stringPredicates},
		{field: "StartTime", inputPrefix: "StartTime", predicates: timePredicates},
		{field: "CloseTime", inputPrefix: "CloseTime", predicates: timePredicates, isNil: where.CloseTimeIsNil, notNil: where.CloseTimeNotNil},
		{field: "pyck_service", inputPrefix: "Service", predicates: stringPredicates, isNil: where.ServiceIsNil, notNil: where.ServiceNotNil, nullIncludesEmpty: true},
		{field: "pyck_data_type", inputPrefix: "DataType", predicates: stringPredicates, isNil: where.DataTypeIsNil, notNil: where.DataTypeNotNil, nullIncludesEmpty: true},
		{field: "pyck_data_id", inputPrefix: "DataID", predicates: stringPredicatesNoFold, isNil: where.DataIDIsNil, notNil: where.DataIDNotNil, nullIncludesEmpty: true},
	}

	var result []string

	for _, m := range mappings {
		result = append(result, m.nullPredicates()...)
		result = append(result, m.resolvePredicates(whereVal, whereType)...)
	}

	return strings.Join(result, " AND ")
}

// BuildWhereClause recursively builds a Temporal visibility query clause
// from a WorkflowExecutionsWhereInput, handling Or, And, and Not fields.
func BuildWhereClause(where *model.WorkflowExecutionsWhereInput) string {
	if where == nil {
		return ""
	}

	var parts []string

	// Field-level predicates (AND-joined internally)
	if fields := buildFieldPredicates(where); fields != "" {
		parts = append(parts, fields)
	}

	// AND: each sub-clause is AND'd with the rest
	for _, andWhere := range where.And {
		if clause := BuildWhereClause(andWhere); clause != "" {
			parts = append(parts, clause)
		}
	}

	// OR: sub-clauses OR'd together, wrapped in parens for correct grouping
	if len(where.Or) > 0 {
		var orParts []string
		for _, orWhere := range where.Or {
			if clause := BuildWhereClause(orWhere); clause != "" {
				orParts = append(orParts, clause)
			}
		}

		if len(orParts) == 1 {
			parts = append(parts, orParts[0])
		} else if len(orParts) > 1 {
			parts = append(parts, "("+strings.Join(orParts, " OR ")+")")
		}
	}

	// NOT: negated, wrapped in parens
	if where.Not != nil {
		if clause := BuildWhereClause(where.Not); clause != "" {
			parts = append(parts, "NOT ("+clause+")")
		}
	}

	return strings.Join(parts, " AND ")
}

// buildTemporalQuery constructs a Temporal visibility query from the given parameters.
func buildTemporalQuery(tenantIDs []uuid.UUID, where *model.WorkflowExecutionsWhereInput) string {
	var queryBuilder strings.Builder

	// Build tenant ID filter
	namespaces := make([]string, len(tenantIDs))
	for i, tID := range tenantIDs {
		namespaces[i] = tID.String()
	}

	queryBuilder.WriteString(fmt.Sprintf(`pyck_tenant_id IN (%s)`, func() string {
		parts := make([]string, len(namespaces))
		for i, ns := range namespaces {
			parts[i] = fmt.Sprintf("%q", ns)
		}
		return strings.Join(parts, ",")
	}()))

	if where == nil {
		return queryBuilder.String()
	}

	if clause := BuildWhereClause(where); clause != "" {
		queryBuilder.WriteString(" AND ")
		queryBuilder.WriteString(clause)
	}

	return queryBuilder.String()
}

// listSingleTenantExecutionsPage fetches a single page of workflow executions for a single tenant.
// Uses Temporal's native pagination for efficiency.
func (r *queryResolver) listSingleTenantExecutionsPage(ctx context.Context, tenantID uuid.UUID, query string, pageSize int, nextPageToken []byte, orderBy *model.WorkflowExecutionOrder) (*model.WorkflowExecutionInfoConnection, error) {
	workflowClient, err := r.workflowRouter.GetClient(ctx, tenantID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow client: %w", err)
	}
	if workflowClient == nil {
		return nil, fmt.Errorf("%w for tenant %s", ErrWorkflowClientNotAvailable, tenantID)
	}

	execs, newToken, err := workflowClient.ListWorkflowsPage(ctx, query, pageSize, nextPageToken)
	if err != nil {
		return nil, err
	}

	executionInfos := make([]*model.WorkflowExecutionInfo, 0, len(execs))
	for _, exec := range execs {
		execInfo := model.WorkflowExecutionInfo{}
		if err := execInfo.FromProto(exec, nil); err != nil {
			return nil, err
		}
		executionInfos = append(executionInfos, &execInfo)
	}

	sortExecutions(executionInfos, orderBy)

	edges := make([]*model.WorkflowExecutionInfoEdge, len(executionInfos))
	for i, exec := range executionInfos {
		edges[i] = &model.WorkflowExecutionInfoEdge{
			Node:   exec,
			Cursor: exec.Execution.WorkflowID + ":" + exec.Execution.ID,
		}
	}

	// Build page info with NextPageToken
	// Note: HasPreviousPage indicates if a cursor was provided (forward-only pagination).
	// Backward pagination is not supported with Temporal's cursor-based pagination.
	var endCursor *string
	hasNextPage := len(newToken) > 0
	if hasNextPage {
		encoded := base64.StdEncoding.EncodeToString(newToken)
		endCursor = &encoded
	}

	return &model.WorkflowExecutionInfoConnection{
		Edges: edges,
		PageInfo: &model.WorkflowExecutionPageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: nextPageToken != nil, // True if cursor was provided; backward navigation not supported
			EndCursor:       endCursor,
		},
		TotalCount: len(edges), // Page count only; Temporal doesn't provide true total count efficiently
	}, nil
}

// listWorkflowExecutionsPage fetches a single page of workflow executions.
// Returns executions, next page token (base64 encoded), and whether there are more pages.
// Note: Pagination only works correctly with a single tenant. Multi-tenant queries
// fetch all results and ignore the after cursor.
func (r *queryResolver) listWorkflowExecutionsPage(ctx context.Context, where *model.WorkflowExecutionsWhereInput, first *int, after *string, orderBy *model.WorkflowExecutionOrder) (*model.WorkflowExecutionInfoConnection, error) {
	req := request.ForContext(ctx)
	tenantIDs := req.TenantIDs()

	pageSize := defaultPageSize
	if first != nil && *first > 0 && *first <= maxPageSize {
		pageSize = *first
	}

	// Decode the after cursor (NextPageToken from Temporal)
	var nextPageToken []byte
	if after != nil && *after != "" {
		decoded, err := base64.StdEncoding.DecodeString(*after)
		if err != nil {
			return nil, fmt.Errorf("invalid pagination cursor: %w", err)
		}
		nextPageToken = decoded
	}

	query := buildTemporalQuery(tenantIDs, where)

	// For single tenant, use proper pagination
	if len(tenantIDs) == 1 {
		return r.listSingleTenantExecutionsPage(ctx, tenantIDs[0], query, pageSize, nextPageToken, orderBy)
	}

	// For multiple tenants, fetch all and paginate in memory (no cursor support)
	executions, err := r.listWorkflowExecutions(ctx, where, 0)
	if err != nil {
		return nil, err
	}

	sortExecutions(executions, orderBy)

	// Apply limit
	if pageSize > 0 && len(executions) > pageSize {
		executions = executions[:pageSize]
	}

	// Build edges
	edges := make([]*model.WorkflowExecutionInfoEdge, len(executions))
	for i, exec := range executions {
		edges[i] = &model.WorkflowExecutionInfoEdge{
			Node:   exec,
			Cursor: exec.Execution.WorkflowID + ":" + exec.Execution.ID,
		}
	}

	return &model.WorkflowExecutionInfoConnection{
		Edges: edges,
		PageInfo: &model.WorkflowExecutionPageInfo{
			HasNextPage:     false,
			HasPreviousPage: false,
		},
		TotalCount: len(edges), // Page count only for multi-tenant queries
	}, nil
}

// listWorkflowExecutions fetches workflow executions from all tenants.
// If limit is 0, fetches all executions. Otherwise, stops after collecting limit executions.
// Used for multi-tenant queries and for workflowHistory.
func (r *queryResolver) listWorkflowExecutions(ctx context.Context, where *model.WorkflowExecutionsWhereInput, limit int) ([]*model.WorkflowExecutionInfo, error) {
	req := request.ForContext(ctx)
	query := buildTemporalQuery(req.TenantIDs(), where)

	group, groupCtx := errgroup.WithContext(ctx)

	var (
		mu             sync.Mutex
		executionInfos []*model.WorkflowExecutionInfo
	)

	for _, tenantID := range req.TenantIDs() {
		group.Go(func() error {
			return r.fetchTenantExecutions(groupCtx, tenantID, query, limit, &mu, &executionInfos)
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	return executionInfos, nil
}

// fetchTenantExecutions fetches workflow executions for a single tenant.
// If limit > 0, stops fetching once the combined results reach the limit.
func (r *queryResolver) fetchTenantExecutions(ctx context.Context, tenantID uuid.UUID, query string, limit int, mu *sync.Mutex, executionInfos *[]*model.WorkflowExecutionInfo) error {
	workflowClient, err := r.workflowRouter.GetClient(ctx, tenantID.String())
	if err != nil {
		return fmt.Errorf("failed to get workflow client for tenant %s: %w", tenantID, err)
	}
	if workflowClient == nil {
		return nil // Client not available for this tenant, skip silently
	}

	if limit > 0 {
		return r.fetchTenantExecutionsWithLimit(ctx, workflowClient, query, limit, mu, executionInfos)
	}

	// No limit - fetch all
	execs, err := workflowClient.ListWorkflows(ctx, query)
	if err != nil {
		return err
	}

	for _, exec := range execs {
		execInfo := model.WorkflowExecutionInfo{}
		if err := execInfo.FromProto(exec, nil); err != nil {
			return err
		}

		mu.Lock()
		*executionInfos = append(*executionInfos, &execInfo)
		mu.Unlock()
	}

	return nil
}

// fetchTenantExecutionsWithLimit fetches workflow executions with a limit, stopping early when limit is reached.
func (r *queryResolver) fetchTenantExecutionsWithLimit(ctx context.Context, wfClient *commonworkflow.Client, query string, limit int, mu *sync.Mutex, executionInfos *[]*model.WorkflowExecutionInfo) error {
	var pageToken []byte

	for {
		mu.Lock()
		currentCount := len(*executionInfos)
		mu.Unlock()

		if currentCount >= limit {
			return nil
		}

		pageSize := min(defaultPageSize, limit-currentCount)

		execs, newToken, err := wfClient.ListWorkflowsPage(ctx, query, pageSize, pageToken)
		if err != nil {
			return err
		}

		for _, exec := range execs {
			execInfo := model.WorkflowExecutionInfo{}
			if err := execInfo.FromProto(exec, nil); err != nil {
				return err
			}

			mu.Lock()
			if len(*executionInfos) < limit {
				*executionInfos = append(*executionInfos, &execInfo)
			}
			mu.Unlock()
		}

		if len(newToken) == 0 {
			return nil
		}
		pageToken = newToken
	}
}

// isNotFoundError checks if an error is a Temporal "not found" type error.
func isNotFoundError(err error) bool {
	var notFoundErr *serviceerror.NotFound
	return errors.As(err, &notFoundErr)
}

// getWorkflowHistory fetches all history events for a single workflow execution.
// Returns ErrWorkflowNotFound if the workflow is not found in any tenant.
// Propagates other errors (network issues, permission problems, etc.) for easier debugging.
func (r *queryResolver) getWorkflowHistory(ctx context.Context, workflowID, runID string) ([]*model.WorkflowEvent, error) {
	req := request.ForContext(ctx)

	for _, tenantID := range req.TenantIDs() {
		workflowClient, err := r.workflowRouter.GetClient(ctx, tenantID.String())
		if err != nil {
			// Propagate client errors for debugging
			return nil, fmt.Errorf("failed to get workflow client for tenant %s: %w", tenantID, err)
		}
		if workflowClient == nil {
			// Client not available for this tenant, try next
			continue
		}

		hist, err := workflowClient.GetWorkflowHistory(ctx, workflowID, runID)
		if err != nil {
			// Only continue to next tenant on "not found" errors
			// Propagate other errors (network issues, permission problems, etc.)
			if isNotFoundError(err) {
				continue
			}
			return nil, fmt.Errorf("failed to get workflow history for tenant %s: %w", tenantID, err)
		}

		events, err := model.HistoryFromProto(hist)
		if err != nil {
			return nil, err
		}

		return events, nil
	}

	return nil, ErrWorkflowNotFound
}

// filterAssignableExecutions returns only executions whose memo contains
// {"data": {"assignable": true}}. This replaces the previous N+1 RPC approach
// that queried each workflow individually.
func filterAssignableExecutions(executions []*model.WorkflowExecutionInfo) []*model.WorkflowExecutionInfo {
	var filtered []*model.WorkflowExecutionInfo
	for _, exec := range executions {
		if exec.Memo != nil && exec.Memo.Data != nil {
			if assignable, ok := exec.Memo.Data["assignable"].(bool); ok && assignable {
				filtered = append(filtered, exec)
			}
		}
	}
	return filtered
}

// listAssignableWorkflowExecutionsPage fetches workflow executions and filters to only those
// with ActivityCount > 0 in their UserDataInput (i.e., workflows that have user tasks).
func (r *queryResolver) listAssignableWorkflowExecutionsPage(ctx context.Context, where *model.WorkflowExecutionsWhereInput, first *int, after *string, orderBy *model.WorkflowExecutionOrder) (*model.WorkflowExecutionInfoConnection, error) {
	req := request.ForContext(ctx)
	tenantIDs := req.TenantIDs()

	pageSize := defaultPageSize
	if first != nil && *first > 0 && *first <= maxPageSize {
		pageSize = *first
	}

	query := buildTemporalQuery(tenantIDs, where)

	// For single tenant, use iterative over-fetching with Temporal pagination
	if len(tenantIDs) == 1 {
		return r.listSingleTenantAssignablePage(ctx, tenantIDs[0], query, pageSize, after, orderBy)
	}

	// For multiple tenants, fetch all, filter, sort, and paginate in memory
	executions, err := r.listWorkflowExecutions(ctx, where, 0)
	if err != nil {
		return nil, err
	}

	// Filter by memo — no RPCs needed
	allFiltered := filterAssignableExecutions(executions)
	totalCount := len(allFiltered)

	sortExecutions(allFiltered, orderBy)

	// Apply limit
	hasNextPage := pageSize > 0 && len(allFiltered) > pageSize
	if hasNextPage {
		allFiltered = allFiltered[:pageSize]
	}

	edges := make([]*model.WorkflowExecutionInfoEdge, len(allFiltered))
	for i, exec := range allFiltered {
		edges[i] = &model.WorkflowExecutionInfoEdge{
			Node:   exec,
			Cursor: encodeAssignableOffset(i + 1),
		}
	}

	var endCursor *string
	if hasNextPage {
		cursor := encodeAssignableOffset(pageSize)
		endCursor = &cursor
	}

	return &model.WorkflowExecutionInfoConnection{
		Edges: edges,
		PageInfo: &model.WorkflowExecutionPageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: false,
			EndCursor:       endCursor,
		},
		TotalCount: totalCount,
	}, nil
}

// encodeAssignableOffset encodes an offset into a base64 cursor string.
func encodeAssignableOffset(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s%d", assignableOffsetPrefix, offset)))
}

// decodeAssignableOffset decodes an offset cursor. Returns 0 if the cursor is not an offset cursor.
func decodeAssignableOffset(cursor string) (int, bool) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0, false
	}

	s := string(decoded)
	if !strings.HasPrefix(s, assignableOffsetPrefix) {
		return 0, false
	}

	var offset int
	if _, err := fmt.Sscanf(s[len(assignableOffsetPrefix):], "%d", &offset); err != nil {
		return 0, false
	}

	return offset, true
}

// listSingleTenantAssignablePage fetches and filters assignable workflows for a single tenant.
// It fetches all pages to compute an accurate totalCount, then paginates in-memory using
// offset-based cursors.
func (r *queryResolver) listSingleTenantAssignablePage(ctx context.Context, tenantID uuid.UUID, query string, pageSize int, after *string, orderBy *model.WorkflowExecutionOrder) (*model.WorkflowExecutionInfoConnection, error) {
	workflowClient, err := r.workflowRouter.GetClient(ctx, tenantID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow client: %w", err)
	}
	if workflowClient == nil {
		return nil, fmt.Errorf("%w for tenant %s", ErrWorkflowClientNotAvailable, tenantID)
	}

	// Decode offset from cursor if present
	startOffset := 0
	if after != nil && *after != "" {
		if offset, ok := decodeAssignableOffset(*after); ok {
			startOffset = offset
		}
	}

	// Fetch all pages to get accurate totalCount
	var (
		allAssignable []*model.WorkflowExecutionInfo
		nextPageToken []byte
	)

	for {
		execs, newToken, err := workflowClient.ListWorkflowsPage(ctx, query, defaultPageSize, nextPageToken)
		if err != nil {
			return nil, err
		}

		// Convert protos to model
		batch := make([]*model.WorkflowExecutionInfo, 0, len(execs))
		for _, exec := range execs {
			execInfo := model.WorkflowExecutionInfo{}
			if err := execInfo.FromProto(exec, nil); err != nil {
				return nil, err
			}
			batch = append(batch, &execInfo)
		}

		// Filter for assignable
		allAssignable = append(allAssignable, filterAssignableExecutions(batch)...)

		if len(newToken) == 0 {
			break
		}

		nextPageToken = newToken
	}

	totalCount := len(allAssignable)

	sortExecutions(allAssignable, orderBy)

	// Apply offset
	if startOffset >= len(allAssignable) {
		return &model.WorkflowExecutionInfoConnection{
			Edges: []*model.WorkflowExecutionInfoEdge{},
			PageInfo: &model.WorkflowExecutionPageInfo{
				HasNextPage:     false,
				HasPreviousPage: startOffset > 0,
			},
			TotalCount: totalCount,
		}, nil
	}
	allAssignable = allAssignable[startOffset:]

	// Trim to page size
	hasNextPage := len(allAssignable) > pageSize
	if hasNextPage {
		allAssignable = allAssignable[:pageSize]
	}

	edges := make([]*model.WorkflowExecutionInfoEdge, len(allAssignable))
	for i, exec := range allAssignable {
		edges[i] = &model.WorkflowExecutionInfoEdge{
			Node:   exec,
			Cursor: encodeAssignableOffset(startOffset + i + 1),
		}
	}

	var endCursor *string
	if hasNextPage {
		cursor := encodeAssignableOffset(startOffset + pageSize)
		endCursor = &cursor
	}

	return &model.WorkflowExecutionInfoConnection{
		Edges: edges,
		PageInfo: &model.WorkflowExecutionPageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: startOffset > 0,
			EndCursor:       endCursor,
		},
		TotalCount: totalCount,
	}, nil
}

// buildExecutionHistoryConnection creates a WorkflowExecutionHistoryConnection from histories.
func buildExecutionHistoryConnection(histories []*model.WorkflowExecutionHistory) *model.WorkflowExecutionHistoryConnection {
	totalCount := len(histories)

	edges := make([]*model.WorkflowExecutionHistoryEdge, len(histories))
	for i, hist := range histories {
		edges[i] = &model.WorkflowExecutionHistoryEdge{
			Node:   hist,
			Cursor: hist.Execution.WorkflowID + ":" + hist.Execution.ID,
		}
	}

	return &model.WorkflowExecutionHistoryConnection{
		Edges: edges,
		PageInfo: &model.WorkflowPageInfo{
			HasNextPage:     false,
			HasPreviousPage: false,
		},
		TotalCount: totalCount,
	}
}
