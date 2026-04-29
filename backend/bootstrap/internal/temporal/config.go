package temporal

// Configuration holds Temporal bootstrap settings loaded from environment variables.
type Configuration struct {
	// TemporalUrl is the Temporal server URL.
	TemporalUrl string `env:"PYCK_TEMPORAL_URL,notEmpty,required"`
}
