package minio

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// bootstrap ensures the configured S3 bucket exists in MinIO.
func bootstrap(ctx context.Context, config *Configuration) error {
	logger := log.ForContext(ctx)

	client, err := newMinIOClient(config)
	if err != nil {
		return fmt.Errorf("failed to create minio client: %w", err)
	}

	// Check if bucket exists
	exists, err := client.BucketExists(ctx, config.Bucket)
	if err != nil {
		return fmt.Errorf("failed to check if bucket exists: %w", err)
	}

	if exists {
		logger.Debug().
			Str("bucket", config.Bucket).
			Msg("Bucket already exists")
		return nil
	}

	// Create bucket
	err = client.MakeBucket(ctx, config.Bucket, minio.MakeBucketOptions{
		Region: config.Region,
	})
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}

	logger.Debug().
		Str("bucket", config.Bucket).
		Msg("Bucket created successfully")

	return nil
}

// newMinIOClient creates a new MinIO client from the configuration.
func newMinIOClient(config *Configuration) (*minio.Client, error) {
	// Parse endpoint to extract host:port and scheme
	parsedEndpoint, err := url.Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse endpoint URL: %w", err)
	}

	endpoint := parsedEndpoint.Host
	if endpoint == "" {
		// If no scheme was provided, use the whole string as host
		endpoint = config.Endpoint
	}

	// Initialize MinIO client
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure: strings.EqualFold(parsedEndpoint.Scheme, "https"),
		Region: config.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize minio client: %w", err)
	}

	return client, nil
}
