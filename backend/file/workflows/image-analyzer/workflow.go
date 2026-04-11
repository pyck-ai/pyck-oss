package imageanalyzer

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func ImageAnalyzeWorkflow(context workflow.Context, input ImageAnalyzerWorkflowInput) (*ImageAnalyzerWorkflowOutput, error) {
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    1 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    1 * time.Minute,
			MaximumAttempts:    3,
		},
	}
	ctx := workflow.WithActivityOptions(context, activityOptions)

	analyzeImageInput := analyzeImageActivityInput(input)
	var analyzeImageOutput analyzeImageActivityOutput
	err := workflow.ExecuteActivity(ctx, AnalyzeImageActivity, analyzeImageInput).Get(ctx, &analyzeImageOutput)
	if err != nil {
		return nil, err
	}

	return &ImageAnalyzerWorkflowOutput{JsonData: analyzeImageOutput.JsonData}, nil
}
