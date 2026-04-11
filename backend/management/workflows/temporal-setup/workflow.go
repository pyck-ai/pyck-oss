package temporalsetup

import (
	"fmt"
	"time"

	"github.com/pyck-ai/pyck/backend/management/core"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func TemporalSetupWorkflow(wCtx workflow.Context, input TemporalSetupWorkflowInput) error {
	for _, attributeType := range input.SearchAttributes {
		_, ok := SearchAttributeTypes[attributeType]
		if !ok {
			return fmt.Errorf("invalid search attribute type: %s", attributeType)
		}
	}

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

	// Add Search Attributes
	addSearchAttributesInput := addSearchAttributesInput{
		TemporalUrl:      core.BootstrapConfig.TemporalUrl,
		Namespace:        input.Namespace,
		SearchAttributes: input.SearchAttributes,
	}
	err := workflow.ExecuteActivity(ctx, AddSearchAttributes, addSearchAttributesInput).Get(ctx, nil)
	if err != nil {
		return err
	}
	return nil
}
