package services

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/test/mocks"
)

func TestS3Service(t *testing.T) {
	mockMinioClient := mocks.NewMockMinioClient()

	t.Run("test new service", func(t *testing.T) {
		// We'll only test the invalid URL case since the connection test is flaky
		_, err := NewS3StorageService("my-bucket", "access-key", "secret-key", "http://localhost:9000", "us-east-1", "h\ntp:/invalid-url")
		assert.Error(t, err)
	})

	t.Run("success create file key", func(t *testing.T) {
		s3Service := &S3StorageService{
			Bucket:       "my-bucket",
			AccessKey:    "access-key",
			SecretKey:    "secret-key",
			MinioClient:  mockMinioClient,
			HTTPEndpoint: "localhost:9000",
			HTTPScheme:   "http",
		}

		tenantID := uuid.New()
		refID := uuid.New()
		filename := "example.pdf"
		expected := tenantID.String() + "/" + refID.String() + "/" + filename
		key := s3Service.CreateFileKey(tenantID, refID, filename)
		assert.Equal(t, expected, key)
	})

	t.Run("success create pre-signed-upload-url", func(t *testing.T) {
		s3Service := &S3StorageService{
			Bucket:                   "my-bucket",
			AccessKey:                "access-key",
			SecretKey:                "secret-key",
			MinioClient:              mockMinioClient,
			HTTPEndpoint:             "localhost:9000",
			HTTPScheme:               "http",
			expiryPreSignedUploadURL: 15 * time.Minute,
		}

		url, err := s3Service.GetPreSignedUploadURL(uuid.New(), "example.pdf", uuid.New(), "application/pdf")
		assert.NoError(t, err)
		assert.NotEmpty(t, url)
	})

	t.Run("success create pre-signed-url", func(t *testing.T) {
		s3Service := &S3StorageService{
			Bucket:             "my-bucket",
			AccessKey:          "access-key",
			SecretKey:          "secret-key",
			MinioClient:        mockMinioClient,
			HTTPEndpoint:       "localhost:9000",
			HTTPScheme:         "http",
			expiryPreSignedURL: 15 * time.Minute,
		}

		url, err := s3Service.GetPreSignedURL(uuid.New(), uuid.New(), "example.pdf")
		assert.NoError(t, err)
		assert.NotEmpty(t, url)
	})

	t.Run("success delete file", func(t *testing.T) {
		s3Service := &S3StorageService{
			Bucket:      "my-bucket",
			AccessKey:   "access-key",
			SecretKey:   "secret-key",
			MinioClient: mockMinioClient,
		}

		err := s3Service.DeleteFile(uuid.New(), uuid.New(), "example.pdf")
		assert.NoError(t, err)
	})

	t.Run("success stat object", func(t *testing.T) {
		t.Parallel()
		mc := mocks.NewMockMinioClient()
		mc.SetStatObjectInfo(minio.ObjectInfo{Size: 5000})
		s3Service := &S3StorageService{
			Bucket:      "my-bucket",
			MinioClient: mc,
		}

		info, err := s3Service.StatObject(uuid.New(), uuid.New(), "example.pdf")
		require.NoError(t, err)
		assert.Equal(t, int64(5000), info.Size)
	})

	t.Run("stat object returns ErrObjectNotFound when missing", func(t *testing.T) {
		t.Parallel()
		mc := mocks.NewMockMinioClient()
		mc.SetStatObjectError(minio.ErrorResponse{Code: "NoSuchKey", StatusCode: http.StatusNotFound})
		s3Service := &S3StorageService{
			Bucket:      "my-bucket",
			MinioClient: mc,
		}

		_, err := s3Service.StatObject(uuid.New(), uuid.New(), "example.pdf")
		require.ErrorIs(t, err, ErrObjectNotFound)
	})
}
