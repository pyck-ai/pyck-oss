package openai

import "github.com/openai/openai-go"

const (
	jsonSchemaGeneratorConfigName   = "json-schema-generator"
	jsonSchemaGeneratorInstructions = "Generate a JSON schema v7 from provided JSON data. The schema must include a title, a description, schema version, schema id, type, properties, required.\n\n# Steps\n\n1. **Analyze JSON Data**: Examine the provided JSON data structure to understand the types, possible values, and hierarchical relationships.\n2. **Define Structure**: Identify all the keys, their data types, and any nested structures.\n3. **Generate Schema Skeleton**: Create the basic structure of the schema including types for each key (object, array, string, number, etc.).\n4. **Include Metadata**: Add a title and description to the schema. These should provide context or an overview of the JSON data's purpose.\n5. **Finalize Schema**: Review the generated schema for completeness and accuracy.\n\n# Output Format\n\nOutput the JSON schema as a well-formatted JSON object, where:\n- The schema includes fields for `title` and `description`.\n- Each key in the JSON data is detailed with corresponding types and structure.\n\n# Examples\n\n## Example Input\n```json\n{\n  \"name\": \"John Doe\",\n  \"age\": 30,\n  \"email\": \"john.doe@example.com\"\n}\n```\n\n## Example Output\n```json\n{\n  \"$schema\": \"http://json-schema.org/draft-07/schema#\",\n  \"$id\": \"http://json-schema.org/draft-07/schema#\",\n  \"title\": \"User Information Schema\",\n  \"description\": \"A schema representing a user's basic information including name, age, and email.\",\n  \"type\": \"object\",\n  \"properties\": {\n    \"name\": {\n      \"type\": \"string\"\n    },\n    \"age\": {\n      \"type\": \"integer\"\n    },\n    \"email\": {\n      \"type\": \"string\",\n      \"format\": \"email\"\n    }\n  },\n  \"required\": [\"name\", \"age\", \"email\"]\n}\n```\n\n## Notes\n\n- Ensure that types are precise—e.g., use `\"integer\"` for whole numbers, `\"string\"` for textual data, and specify formats like `\"email\"`.\n- Consider optional fields in the JSON data, but ensure that required fields are identified accurately. \n- Titles and descriptions should be clear and concise, providing an overview of the purpose and use of the JSON data."
	jsonDataGeneratorConfigName     = "json-data-generator"
	jsonDataGeneratorInstructions   = "Process the provided file or image to extract specific information and output it in a user-defined JSON response format.\n\n# Steps\n\n1. **Identify the Input Type**: Determine if the input is a file or an image.\n2. **Extract Relevant Information**: Analyze the file or image content to locate and extract the required data. This may involve:\n   - For text files, reading lines, parsing structured data, or extracting keywords.\n   - For images, performing text recognition (OCR) or identifying specified objects or patterns.\n3. **Match User Requirements**: Review the user's provided JSON response format to ensure alignment with the extracted data.\n4. **Convert to JSON Format**: Organize the extracted information according to the user-defined JSON structure. Ensure every required field is included and correctly populated.\n\n# Output Format\n\n- The output should be a JSON object structured as per the user-defined format.\n- Ensure all fields are correctly labeled and data is appropriately inserted or nullified if absent.\n\n# Examples\n\n**Example 1:**\n\n**Input:** \n- Image: A scanned receipt with store name, date, total amount.\n\n**User-defined JSON format:**\n```json\n{\n  \"store_name\": \"\",\n  \"date\": \"\",\n  \"total_amount\": \"\"\n}\n```\n\n**Output:**\n```json\n{\n  \"store_name\": \"SuperMart\",\n  \"date\": \"2023-11-05\",\n  \"total_amount\": \"24.99\"\n}\n```\n\n**Example 2:**\n\n**Input:** \n- File: Text document with student information including name, age, grade.\n\n**User-defined JSON format:**\n```json\n{\n  \"name\": \"\",\n  \"age\": \"\",\n  \"grade\": \"\"\n}\n```\n\n**Output:**\n```json\n{\n  \"name\": \"John Doe\",\n  \"age\": \"16\",\n  \"grade\": \"10th\"\n}\n```\n\n# Notes\n\n- Verify image OCR accuracy by cross-referencing extracted text with expected text patterns.\n- Adapt to different image qualities and text file formats ensuring robustness.\n- In cases where information is missing or incomplete in the input, the JSON field should be left empty or marked as \"unknown\" based on user preference."
)

type responseFormatJsonSchema struct {
	Name   string
	Strict bool
	Schema interface{}
}

type responseFormat struct {
	Type       string
	JsonSchema *responseFormatJsonSchema
}

type chatConfig struct {
	instructions   string
	model          openai.ChatModel
	responseFormat responseFormat
	temperature    float64
}

var chatConfigs = map[string]chatConfig{
	jsonSchemaGeneratorConfigName: {
		instructions:   jsonSchemaGeneratorInstructions,
		model:          openai.ChatModelGPT4oMini2024_07_18,
		responseFormat: responseFormat{Type: "json_object"},
		temperature:    0.3,
	},
	jsonDataGeneratorConfigName: {
		instructions: jsonDataGeneratorInstructions,
		model:        openai.ChatModelGPT4oMini2024_07_18,
		responseFormat: responseFormat{
			Type: "json_schema",
			JsonSchema: &responseFormatJsonSchema{
				Name:   "json_data",
				Strict: false,
			},
		},
		temperature: 0.3,
	},
}
