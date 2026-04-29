package minio

// Configuration holds MinIO bootstrap settings loaded from environment variables.
type Configuration struct {
	// Bucket is the S3 bucket name to create
	Bucket string `env:"PYCK_AWS_S3_BUCKET,notEmpty,required"`

	// AccessKey is the MinIO access key
	AccessKey string `env:"PYCK_AWS_ACCESS_KEY_ID,notEmpty,required"`

	// SecretKey is the MinIO secret key
	SecretKey string `env:"PYCK_AWS_SECRET_ACCESS_KEY,notEmpty,required"`

	// Endpoint is the MinIO endpoint URL (e.g., minio:9000)
	Endpoint string `env:"PYCK_AWS_S3_ENDPOINT_URL,notEmpty,required"`

	// Region is the S3 region
	Region string `env:"PYCK_AWS_S3_REGION,notEmpty,required"`
}
