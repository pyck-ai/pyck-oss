package generatejsonschema

type GenerateJSONSchemaWorkflowInput struct {
	OpenAIToken string
	JsonData    string
}

type GenerateJSONSchemaWorkflowOutput struct {
	JsonSchema string
}

type generateSchemaInput struct {
	OpenAIToken string
	JsonData    string
}

type generateSchemaOutput struct {
	JsonSchema string
}
