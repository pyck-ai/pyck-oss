package env

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

func ReadYamlEnv(envPath string, envName string) (map[string]string, error) {
	file, err := os.ReadFile(envPath)
	if err != nil {
		return nil, err
	}

	var envs map[string]map[string]string
	err = yaml.Unmarshal(file, &envs)
	if err != nil {
		return nil, fmt.Errorf("unsupported format in environments.yaml: %w", err)
	}

	env, ok := envs[envName]
	if !ok {
		return nil, fmt.Errorf("environment not found: %s", envName)
	}
	return env, nil
}
