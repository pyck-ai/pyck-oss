package imageanalyzer

type ImageAnalyzerWorkflowInput struct {
	OpenAIToken string
	ImageURL    string
	JsonSchema  string
}

type ImageAnalyzerWorkflowOutput struct {
	JsonData string
}

type analyzeImageActivityInput struct {
	OpenAIToken string
	ImageURL    string
	JsonSchema  string
}

type analyzeImageActivityOutput struct {
	JsonData string
}
