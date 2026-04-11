package generatejsonschema

import (
	"context"

	openai "github.com/pyck-ai/pyck/backend/common/services/open-ai"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

func GenerateJSONSchemaActivity(ctx context.Context, input generateSchemaInput) (*generateSchemaOutput, error) {
	client := openai.NewClient(input.OpenAIToken)

	generatedSchema, err := client.GenerateJsonSchema(ctx, input.JsonData)
	if err != nil {
		return nil, err
	}

	_, err = jsonschema.CompileString("schema.json", generatedSchema)
	if err != nil {
		return nil, err
	}

	return &generateSchemaOutput{JsonSchema: generatedSchema}, nil
}
