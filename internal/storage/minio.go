package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const bucketName = "nimbus-chunks"

type MinioClient struct {
	client *minio.Client
}

func NewMinioClient(endpoint, accessKeyID, secretAccessKey string, useSSL bool) (*MinioClient, error) {
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init minio client: %w", err)
	}

	// Ensure bucket exists
	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to check bucket existence: %w", err)
	}
	if !exists {
		err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	return &MinioClient{client: minioClient}, nil
}

// Ping checks readiness connectivity to MinIO storage cluster
func (m *MinioClient) Ping(ctx context.Context) error {
	_, err := m.client.BucketExists(ctx, bucketName)
	return err
}

func (m *MinioClient) UploadChunk(ctx context.Context, hash string, reader io.Reader, size int64) error {
	_, err := m.client.PutObject(ctx, bucketName, hash, reader, size, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	return err
}

func (m *MinioClient) DownloadChunk(ctx context.Context, hash string) (io.ReadCloser, error) {
	object, err := m.client.GetObject(ctx, bucketName, hash, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return object, nil
}

func (m *MinioClient) DeleteChunk(ctx context.Context, hash string) error {
	return m.client.RemoveObject(ctx, bucketName, hash, minio.RemoveObjectOptions{})
}
