package std

import (
	"encoding/json"
	"strings"
)

func MapToStruct[T any](mmap map[string]interface{}) (T, error) {
	var result T
	mapBytes, err := MarshalJson(mmap)
	if err != nil {
		return result, err
	}

	result, err = UnmarshalJson[T](mapBytes)
	if err != nil {
		return result, err
	}
	return result, nil
}

func InterfaceToMap(input interface{}) (map[string]interface{}, error) {
	if input == nil {
		return map[string]interface{}{}, nil
	}

	if m, ok := input.(map[string]interface{}); ok {
		return m, nil
	}

	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func LowercaseMap(input map[string]interface{}) map[string]interface{} {
	output := make(map[string]interface{}, len(input))

	for originalKey, originalValue := range input {
		lowerKey := strings.ToLower(originalKey)

		if nested, ok := originalValue.(map[string]interface{}); ok {
			output[lowerKey] = LowercaseMap(nested)
		} else {
			output[lowerKey] = originalValue
		}
	}

	return output
}
