package generatejsonschema

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func GenerateJSONSchemaWorkflow(context workflow.Context, input GenerateJSONSchemaWorkflowInput) (*GenerateJSONSchemaWorkflowOutput, error) {
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

	generateSchemaInput := generateSchemaInput(input)
	var generatedSchemaOutput generateSchemaOutput
	err := workflow.ExecuteActivity(ctx, GenerateJSONSchemaActivity, generateSchemaInput).Get(ctx, &generatedSchemaOutput)
	if err != nil {
		return nil, err
	}

	return &GenerateJSONSchemaWorkflowOutput{JsonSchema: generatedSchemaOutput.JsonSchema}, nil
}
