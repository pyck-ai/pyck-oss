package openai

import (
	"context"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/pyck-ai/pyck/backend/common/std"
)

const (
	userMessageTextType  = "text"
	userMessageImageType = "image"
)

type userMessage struct {
	Content string
	Type    string
}

type OpenAIClient struct {
	token        string
	openaiClient *openai.Client
}

func NewClient(token string) *OpenAIClient {
	openaiClient := openai.NewClient(
		option.WithHeader("OpenAI-Beta", "assistants=v2"),
		option.WithAPIKey(token),
	)
	
	client := &OpenAIClient{
		token: token,
		openaiClient: &openaiClient,
	}

	return client
}

func (client *OpenAIClient) GenerateJsonSchema(ctx context.Context, jsonDataString string) (string, error) {
	userMessage := userMessage{
		Content: jsonDataString,
		Type:    userMessageTextType,
	}

	chatParams, err := client.chatCompletionParams(chatConfigs[jsonSchemaGeneratorConfigName], userMessage)
	if err != nil {
		return "", err
	}

	chat, err := client.openaiClient.Chat.Completions.New(ctx, *chatParams)
	if err != nil {
		return "", err
	}

	if len(chat.Choices) == 0 {
		return "", fmt.Errorf("no completion choices returned")
	}

	return chat.Choices[0].Message.Content, nil
}

func (client *OpenAIClient) GenerateJsonDataFromImage(ctx context.Context, jsonSchema string, imageUrl string) (string, error) {
	schemaMap, err := std.UnmarshalJson[map[string]interface{}]([]byte(jsonSchema))
	if err != nil {
		return "", err
	}

	defaultConfig := chatConfigs[jsonDataGeneratorConfigName]
	chatConfig := chatConfig{
		model:        defaultConfig.model,
		instructions: defaultConfig.instructions,
		responseFormat: responseFormat{
			Type: defaultConfig.responseFormat.Type,
			JsonSchema: &responseFormatJsonSchema{
				Name:   defaultConfig.responseFormat.JsonSchema.Name,
				Strict: defaultConfig.responseFormat.JsonSchema.Strict,
				Schema: schemaMap,
			},
		},
	}

	userMessage := userMessage{
		Content: imageUrl,
		Type:    userMessageImageType,
	}

	chatParams, err := client.chatCompletionParams(chatConfig, userMessage)
	if err != nil {
		return "", err
	}
	chat, err := client.openaiClient.Chat.Completions.New(ctx, *chatParams)
	if err != nil {
		return "", err
	}
	if len(chat.Choices) == 0 {
		return "", fmt.Errorf("no completion choices returned")
	}
	return chat.Choices[0].Message.Content, nil
}

func (client *OpenAIClient) chatCompletionParams(config chatConfig, userMassage userMessage) (*openai.ChatCompletionNewParams, error) {
	responseFormatReq, err := client.chatCompletionResponseFormat(config.responseFormat)
	if err != nil {
		return nil, err
	}

	var userRequest openai.ChatCompletionMessageParamUnion
	switch userMassage.Type {
	case userMessageTextType:
		userRequest = openai.UserMessage(userMassage.Content)
	case userMessageImageType:
		imageContent := openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
			URL: userMassage.Content,
		})
		userRequest = openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{imageContent})
	default:
		return nil, fmt.Errorf("unsupported user message type: %s", userMassage.Type)
	}

	chatMessages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(config.instructions),
		userRequest,
	}

	return &openai.ChatCompletionNewParams{
		Model:          config.model,
		Messages:       chatMessages,
		ResponseFormat: responseFormatReq,
	}, nil
}

func (client *OpenAIClient) chatCompletionResponseFormat(responseFormat responseFormat) (openai.ChatCompletionNewParamsResponseFormatUnion, error) {
	switch responseFormat.Type {
	case "text":
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfText: &openai.ResponseFormatTextParam{},
		}, nil
	case "json_object":
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
		}, nil
	case "json_schema":
		if responseFormat.JsonSchema == nil {
			return openai.ChatCompletionNewParamsResponseFormatUnion{}, fmt.Errorf("JsonSchema is nil")
		}

		jsonSchemaParam := openai.ResponseFormatJSONSchemaJSONSchemaParam{
			Name:   responseFormat.JsonSchema.Name,
			Strict: openai.Bool(responseFormat.JsonSchema.Strict),
			Schema: responseFormat.JsonSchema.Schema,
		}

		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
				JSONSchema: jsonSchemaParam,
			},
		}, nil
	default:
		return openai.ChatCompletionNewParamsResponseFormatUnion{}, fmt.Errorf("unsupported response format type: %s", responseFormat.Type)
	}
}
