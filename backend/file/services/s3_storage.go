package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/rs/zerolog/log"
)

// MinioClientInterface defines the interface for MinIO client operations
type MinioClientInterface interface {
	BucketExists(ctx context.Context, bucketName string) (bool, error)
	MakeBucket(ctx context.Context, bucketName string, opts minio.MakeBucketOptions) error
	RemoveObject(ctx context.Context, bucketName, objectName string, opts minio.RemoveObjectOptions) error
	PresignedPutObject(ctx context.Context, bucketName, objectName string, expires time.Duration) (*url.URL, error)
	PresignedGetObject(ctx context.Context, bucketName, objectName string, expires time.Duration, reqParams url.Values) (*url.URL, error)
	StatObject(ctx context.Context, bucketName, objectName string, opts minio.StatObjectOptions) (minio.ObjectInfo, error)
}

// ErrObjectNotFound is returned when StatObject cannot find the object in storage,
// typically because the upload has not yet completed.
var ErrObjectNotFound = errors.New("object not found in storage")

type S3StorageService struct {
	Bucket                   string
	AccessKey                string
	SecretKey                string
	Region                   string
	HTTPScheme               string
	HTTPEndpoint             string
	MinioClient              MinioClientInterface
	expiryPreSignedUploadURL time.Duration
	expiryPreSignedURL       time.Duration
}

func NewS3StorageService(bucket, accessKey, secretKey, endpoint, region, httpEndpointURL string) (*S3StorageService, error) {
	log.Debug().
		Str("bucket", bucket).
		Str("endpoint", endpoint).
		Str("region", region).
		Str("httpEndpointURL", httpEndpointURL).
		Msg("Initializing S3 storage service")

	// Parse the endpoint URL to determine if SSL should be used
	useSSL := strings.HasPrefix(endpoint, "https")

	// Parse the endpoint to remove the scheme
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil {
		log.Error().Err(err).Str("endpoint", endpoint).Msg("Error parsing endpoint URL")
		return nil, fmt.Errorf("error parsing endpoint URL: %v", err)
	}

	// Extract the hostname and port
	endpointHost := parsedEndpoint.Host

	log.Debug().
		Str("endpointHost", endpointHost).
		Bool("useSSL", useSSL).
		Msg("Minio config settings")

	// Initialize MinIO client
	minioClient, err := minio.New(endpointHost, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		log.Error().Err(err).Msg("Error creating new MinIO client")
		return nil, fmt.Errorf("error creating new MinIO client: %v", err)
	}

	// handle URL used for calling file
	log.Debug().Str("httpEndpointURL", httpEndpointURL).Msg("Parsing S3 HTTP endpoint URL")
	parsedURL, err := url.Parse(httpEndpointURL)
	if err != nil {
		log.Error().Err(err).Str("httpEndpointURL", httpEndpointURL).Msg("Error parsing s3 http endpoint URL")
		return nil, errors.New("error parsing s3 http endpoint URL")
	}

	httpEndpointScheme := parsedURL.Scheme
	httpEndpoint := parsedURL.Hostname()
	httpEndpointPort := parsedURL.Port()
	if httpEndpointPort != "" {
		httpEndpoint = httpEndpoint + ":" + httpEndpointPort
	}

	log.Debug().
		Str("httpEndpointScheme", httpEndpointScheme).
		Str("httpEndpoint", httpEndpoint).
		Str("httpEndpointPort", httpEndpointPort).
		Msg("Parsed HTTP endpoint details")

	return &S3StorageService{
		Bucket:                   bucket,
		AccessKey:                accessKey,
		SecretKey:                secretKey,
		MinioClient:              minioClient,
		HTTPEndpoint:             httpEndpoint,
		HTTPScheme:               httpEndpointScheme,
		expiryPreSignedUploadURL: 15 * time.Minute,
		expiryPreSignedURL:       15 * time.Minute,
	}, nil
}

func (s *S3StorageService) CreateFileKey(tenantID, refID uuid.UUID, filename string) string {
	key := fmt.Sprintf("%s/%s/%s", tenantID, refID, filename)
	log.Debug().
		Str("tenantID", tenantID.String()).
		Str("refID", refID.String()).
		Str("filename", filename).
		Str("key", key).
		Msg("Created file key")
	return key
}

func (s *S3StorageService) GetPreSignedUploadURL(tenantID uuid.UUID, filename string, refID uuid.UUID, contentType string) (string, error) {
	log.Debug().
		Str("tenantID", tenantID.String()).
		Str("refID", refID.String()).
		Str("filename", filename).
		Str("contentType", contentType).
		Str("bucket", s.Bucket).
		Msg("Getting pre-signed upload URL")

	userFileName := s.CreateFileKey(tenantID, refID, filename)

	log.Debug().
		Str("userFileName", userFileName).
		Str("bucket", s.Bucket).
		Str("contentType", contentType).
		Msg("Creating MinIO PutObject presigned URL")

	ctx := context.Background()

	// Create policy for upload but don't use it for presigning
	// Just keeping this code in case we want to switch to form post uploads later
	/*
		policy := minio.NewPostPolicy()
		policy.SetBucket(s.Bucket)
		policy.SetKey(userFileName)
		policy.SetExpires(time.Now().Add(s.expiryPreSignedUploadURL))
		policy.SetContentType(contentType)
	*/

	// Generate a presigned URL with a 15-minute expiration
	presignedURL, err := s.MinioClient.PresignedPutObject(ctx, s.Bucket, userFileName, s.expiryPreSignedUploadURL)
	if err != nil {
		log.Error().Err(err).Msg("Error generating presigned PUT URL")
		return "", fmt.Errorf("error generating presigned PUT URL: %v", err)
	}

	// If we need to customize the URL host/scheme (like in the original code)
	if s.HTTPEndpoint != "" && s.HTTPScheme != "" {
		presignedURLParsed := presignedURL

		log.Debug().
			Str("original_host", presignedURLParsed.Host).
			Str("original_scheme", presignedURLParsed.Scheme).
			Str("new_host", s.HTTPEndpoint).
			Str("new_scheme", s.HTTPScheme).
			Msg("Modifying presigned URL host and scheme")

		presignedURLParsed.Host = s.HTTPEndpoint
		presignedURLParsed.Scheme = s.HTTPScheme

		log.Debug().
			Str("modified_url", presignedURLParsed.String()).
			Msg("Modified presigned URL")

		presignedURL = presignedURLParsed
	}

	urlStr := presignedURL.String()
	log.Debug().
		Str("presignedURL", urlStr).
		Dur("expiryTime", s.expiryPreSignedUploadURL).
		Msg("Generated pre-signed upload URL")

	parsedURL, parseErr := url.Parse(urlStr)
	if parseErr != nil {
		log.Error().Err(parseErr).Str("urlStr", urlStr).Msg("Failed to parse generated URL")
	} else {
		log.Debug().
			Str("scheme", parsedURL.Scheme).
			Str("host", parsedURL.Host).
			Str("path", parsedURL.Path).
			Str("rawQuery", parsedURL.RawQuery).
			Msg("Parsed components of generated URL")
	}

	return urlStr, nil
}

func (s *S3StorageService) GetPreSignedURL(tenantID, refID uuid.UUID, filename string) (string, error) {
	log.Debug().
		Str("tenantID", tenantID.String()).
		Str("refID", refID.String()).
		Str("filename", filename).
		Str("bucket", s.Bucket).
		Msg("Getting pre-signed URL for download")

	userFileName := s.CreateFileKey(tenantID, refID, filename)

	log.Debug().
		Str("userFileName", userFileName).
		Str("bucket", s.Bucket).
		Msg("Creating MinIO GetObject presigned URL")

	ctx := context.Background()

	// Generate a presigned URL with a 15-minute expiration
	presignedURL, err := s.MinioClient.PresignedGetObject(ctx, s.Bucket, userFileName, s.expiryPreSignedURL, nil)
	if err != nil {
		log.Error().Err(err).Msg("Error generating presigned GET URL")
		return "", fmt.Errorf("error generating presigned GET URL: %v", err)
	}

	// If we need to customize the URL host/scheme (like in the original code)
	if s.HTTPEndpoint != "" && s.HTTPScheme != "" {
		presignedURLParsed := presignedURL

		log.Debug().
			Str("original_host", presignedURLParsed.Host).
			Str("original_scheme", presignedURLParsed.Scheme).
			Str("new_host", s.HTTPEndpoint).
			Str("new_scheme", s.HTTPScheme).
			Msg("Modifying presigned URL host and scheme")

		presignedURLParsed.Host = s.HTTPEndpoint
		presignedURLParsed.Scheme = s.HTTPScheme

		log.Debug().
			Str("modified_url", presignedURLParsed.String()).
			Msg("Modified presigned URL")

		presignedURL = presignedURLParsed
	}

	urlStr := presignedURL.String()
	log.Debug().
		Str("presignedURL", urlStr).
		Dur("expiryTime", s.expiryPreSignedURL).
		Msg("Generated pre-signed URL")

	parsedURL, parseErr := url.Parse(urlStr)
	if parseErr != nil {
		log.Error().Err(parseErr).Str("urlStr", urlStr).Msg("Failed to parse generated URL")
	} else {
		log.Debug().
			Str("scheme", parsedURL.Scheme).
			Str("host", parsedURL.Host).
			Str("path", parsedURL.Path).
			Str("rawQuery", parsedURL.RawQuery).
			Msg("Parsed components of generated URL")
	}

	return urlStr, nil
}

func (s *S3StorageService) DeleteFile(tenantID, refID uuid.UUID, filename string) error {
	log.Debug().
		Str("tenantID", tenantID.String()).
		Str("refID", refID.String()).
		Str("filename", filename).
		Str("bucket", s.Bucket).
		Msg("Deleting file from S3")

	userFileName := s.CreateFileKey(tenantID, refID, filename)

	log.Debug().
		Str("userFileName", userFileName).
		Str("bucket", s.Bucket).
		Msg("Removing object from MinIO")

	ctx := context.Background()
	err := s.MinioClient.RemoveObject(ctx, s.Bucket, userFileName, minio.RemoveObjectOptions{})
	if err != nil {
		log.Error().Err(err).
			Str("userFileName", userFileName).
			Str("bucket", s.Bucket).
			Msg("Error deleting file from S3")
		return fmt.Errorf("error deleting file: %v", err)
	}

	log.Debug().
		Str("userFileName", userFileName).
		Str("bucket", s.Bucket).
		Msg("File deleted successfully")

	return nil
}

// StatObject retrieves object metadata (size, etag, etc.) from storage without
// downloading the content. Returns ErrObjectNotFound if the object does not exist
// yet, which typically means the client-side upload has not completed.
func (s *S3StorageService) StatObject(tenantID, refID uuid.UUID, filename string) (minio.ObjectInfo, error) {
	userFileName := s.CreateFileKey(tenantID, refID, filename)

	log.Debug().
		Str("userFileName", userFileName).
		Str("bucket", s.Bucket).
		Msg("Stat object in MinIO")

	info, err := s.MinioClient.StatObject(context.Background(), s.Bucket, userFileName, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" || errResp.StatusCode == http.StatusNotFound {
			return minio.ObjectInfo{}, fmt.Errorf("%w: %s", ErrObjectNotFound, userFileName)
		}
		log.Error().Err(err).
			Str("userFileName", userFileName).
			Str("bucket", s.Bucket).
			Msg("Error stating object in S3")
		return minio.ObjectInfo{}, fmt.Errorf("error stating object: %w", err)
	}

	return info, nil
}
