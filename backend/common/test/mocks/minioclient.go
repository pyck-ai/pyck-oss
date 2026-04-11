package mocks

import (
	"context"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
)

const (
	testPreSignedMinioUrlPath = "/pyck-local-dev/7cdbc30a-6f27-5aa1-bd4a-e7d5106075a5/test.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=pyck%2F20240822%2Fus-west-2%2Fs3%2Faws4_request&X-Amz-Date=20240822T124816Z&X-Amz-Expires=900&X-Amz-SignedHeaders=content-type%3Bhost&X-Amz-Signature=991f02a2d2323452c6350d95679b9bb9dcb14cc9a40dd5510735f7d28fdab5f1"
)

// MockMinioClient mocks the MinIO client for testing
type MockMinioClient struct {
	putObjectError    error
	getObjectError    error
	deleteObjectError error
	bucketExistsError error
	makeBucketError   error
}

// NewMockMinioClient creates a new mock MinIO client
func NewMockMinioClient() *MockMinioClient {
	return &MockMinioClient{}
}

// BucketExists mocks the BucketExists method
func (m *MockMinioClient) BucketExists(ctx context.Context, bucketName string) (bool, error) {
	if m.bucketExistsError != nil {
		return false, m.bucketExistsError
	}
	return true, nil
}

// MakeBucket mocks the MakeBucket method
func (m *MockMinioClient) MakeBucket(ctx context.Context, bucketName string, opts minio.MakeBucketOptions) error {
	if m.makeBucketError != nil {
		return m.makeBucketError
	}
	return nil
}

// PutObject mocks the PutObject method
func (m *MockMinioClient) PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) (minio.UploadInfo, error) {
	if m.putObjectError != nil {
		return minio.UploadInfo{}, m.putObjectError
	}
	return minio.UploadInfo{
		Bucket:       bucketName,
		Key:          objectName,
		ETag:         "test-etag",
		Size:         objectSize,
		LastModified: time.Now(),
	}, nil
}

// RemoveObject mocks the RemoveObject method
func (m *MockMinioClient) RemoveObject(ctx context.Context, bucketName, objectName string, opts minio.RemoveObjectOptions) error {
	if m.deleteObjectError != nil {
		return m.deleteObjectError
	}
	return nil
}

// PresignedPutObject mocks the PresignedPutObject method
func (m *MockMinioClient) PresignedPutObject(ctx context.Context, bucketName, objectName string, expires time.Duration) (*url.URL, error) {
	if m.putObjectError != nil {
		return nil, m.putObjectError
	}

	testURL, _ := url.Parse("http://localhost:9000" + testPreSignedMinioUrlPath)
	return testURL, nil
}

// PresignedGetObject mocks the PresignedGetObject method
func (m *MockMinioClient) PresignedGetObject(ctx context.Context, bucketName, objectName string, expires time.Duration, reqParams url.Values) (*url.URL, error) {
	if m.getObjectError != nil {
		return nil, m.getObjectError
	}

	testURL, _ := url.Parse("http://localhost:9000" + testPreSignedMinioUrlPath)
	return testURL, nil
}

// SetPutObjectError sets the error for PutObject operations
func (m *MockMinioClient) SetPutObjectError(err error) {
	m.putObjectError = err
}

// SetGetObjectError sets the error for GetObject operations
func (m *MockMinioClient) SetGetObjectError(err error) {
	m.getObjectError = err
}

// SetDeleteObjectError sets the error for DeleteObject operations
func (m *MockMinioClient) SetDeleteObjectError(err error) {
	m.deleteObjectError = err
}

// SetBucketExistsError sets the error for BucketExists operations
func (m *MockMinioClient) SetBucketExistsError(err error) {
	m.bucketExistsError = err
}

// SetMakeBucketError sets the error for MakeBucket operations
func (m *MockMinioClient) SetMakeBucketError(err error) {
	m.makeBucketError = err
}
