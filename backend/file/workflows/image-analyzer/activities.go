package imageanalyzer

import (
	"context"

	openai "github.com/pyck-ai/pyck/backend/common/services/open-ai"
)

func AnalyzeImageActivity(ctx context.Context, input analyzeImageActivityInput) (*analyzeImageActivityOutput, error) {
	client := openai.NewClient(input.OpenAIToken)

	analysis, err := client.GenerateJsonDataFromImage(ctx, input.JsonSchema, input.ImageURL)
	if err != nil {
		return nil, err
	}

	return &analyzeImageActivityOutput{JsonData: analysis}, nil
}
