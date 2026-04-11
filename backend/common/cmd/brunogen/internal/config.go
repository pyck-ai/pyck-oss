package gen

import (
	"gopkg.in/yaml.v3"

	_ "embed"
)

//go:embed brunogen.config.yaml
var configBytes []byte

// brunoConfig holds the embedded brunogen configuration.
type brunoConfig struct {
	// ServiceEnvNames maps a service name (e.g. "main-data") to the env-var
	// suffix used when building Bruno URL variable names
	// (env_baseurl_<suffix>_query).
	ServiceEnvNames map[string]string `yaml:"serviceEnvNames"`
}

var globalConfig = func() brunoConfig {
	var cfg brunoConfig
	if err := yaml.Unmarshal(configBytes, &cfg); err != nil {
		panic("brunogen: failed to parse embedded config: " + err.Error())
	}
	return cfg
}()

// envServiceName returns the Bruno env-var suffix for the given service name.
// If an override is configured, it is returned; otherwise the name is used as-is.
func envServiceName(serviceName string) string {
	if override, ok := globalConfig.ServiceEnvNames[serviceName]; ok {
		return override
	}
	return serviceName
}
