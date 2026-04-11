package test

import (
	"fmt"
)

func LoadSchemaByName(name string) ([]byte, error) {
	fileName := fmt.Sprintf("jsonschemas/%s.json", name)
	return schemaFS.ReadFile(fileName)
}

func MustLoadSchemaByName(name string) []byte {
	data, err := LoadSchemaByName(name)
	if err != nil {
		panic(fmt.Sprintf("failed to load schema %s: %v", name, err))
	}
	return data
}
