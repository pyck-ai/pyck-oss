package checkstock

import (
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"time"
)

func CheckStockConsistencyWorkflow(wCtx workflow.Context, input WorkflowInput) (*WorkflowOutput, error) {
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    3,
		},
	}
	ctx := workflow.WithActivityOptions(wCtx, activityOptions)

	var timeRange TimeRange
	err := workflow.ExecuteActivity(ctx, ComputeTimeRangesActivity).Get(ctx, &timeRange)
	if err != nil {
		return nil, err
	}

	var transactions []Transaction
	err = workflow.ExecuteActivity(ctx, FetchTransactionsActivity, FetchTransactionsInput{
		ApiURL:    input.ApiURL,
		AuthToken: input.AuthToken,
		TimeRange: timeRange,
	}).Get(ctx, &transactions)
	if err != nil {
		return nil, err
	}

	var stockExpectations []StockExpectation
	err = workflow.ExecuteActivity(ctx, ComputeExpectedStockActivity, ComputeExpectedStockInput{
		ApiURL:         input.ApiURL,
		AuthToken:      input.AuthToken,
		Transactions:   transactions,
		EndOfYesterday: timeRange.EndOfYesterday,
	}).Get(ctx, &stockExpectations)
	if err != nil {
		return nil, err
	}

	var discrepancies []Discrepancy
	err = workflow.ExecuteActivity(ctx, CompareActualStockActivity, CompareStockInput{
		ApiURL:       input.ApiURL,
		AuthToken:    input.AuthToken,
		Expectations: stockExpectations,
		EndOfDay:     timeRange.EndOfDay,
	}).Get(ctx, &discrepancies)
	if err != nil {
		return nil, err
	}

	output := WorkflowOutput{
		CheckedAt:          workflow.Now(wCtx),
		TotalChecked:       len(stockExpectations),
		TotalDiscrepancies: len(discrepancies),
		Discrepancies:      discrepancies,
		Failed:             len(discrepancies) > 0,
	}

	if output.Failed {
		return nil, temporal.NewApplicationError(
			"stock consistency check failed",
			"StockCheckFailed",
			output,
		)
	}

	return &output, nil
}
