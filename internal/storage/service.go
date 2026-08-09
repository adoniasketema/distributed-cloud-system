package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"nimbus/internal/events"
	"time"
)

const ChunkSize = 4 * 1024 * 1024 // 4MB

type Service interface {
	UploadFile(ctx context.Context, userID string, folderID *string, name string, fileReader io.Reader) (string, error)
	DownloadFile(ctx context.Context, userID string, fileID string) (io.Reader, string, error)
	DownloadFileInternal(ctx context.Context, fileID string) (io.Reader, error)
	DeleteFile(ctx context.Context, userID string, fileID string) error
	RunGarbageCollection(ctx context.Context, minAge time.Duration) error
}

type ObjectStore interface {
	UploadChunk(ctx context.Context, hash string, reader io.Reader, size int64) error
	DownloadChunk(ctx context.Context, hash string) (io.ReadCloser, error)
	DeleteChunk(ctx context.Context, hash string) error
}

type service struct {
	repo      Repository
	store     ObjectStore
	publisher events.Publisher
}

func NewService(repo Repository, store ObjectStore, publisher events.Publisher) Service {
	return &service{
		repo:      repo,
		store:     store,
		publisher: publisher,
	}
}

type integrityCheckingReader struct {
	r         io.Reader
	expected  string
	hasher    hash.Hash
	err       error
}

func newIntegrityCheckingReader(r io.Reader, expectedHash string) io.Reader {
	return &integrityCheckingReader{
		r:        r,
		expected: expectedHash,
		hasher:   sha256.New(),
	}
}

func (i *integrityCheckingReader) Read(p []byte) (int, error) {
	if i.err != nil {
		return 0, i.err
	}
	n, err := i.r.Read(p)
	if n > 0 {
		i.hasher.Write(p[:n])
	}
	if err == io.EOF {
		sum := hex.EncodeToString(i.hasher.Sum(nil))
		if sum != i.expected {
			i.err = fmt.Errorf("chunk integrity check failed: expected hash %s got %s", i.expected, sum)
			return n, i.err
		}
	}
	return n, err
}

func (s *service) UploadFile(ctx context.Context, userID string, folderID *string, name string, fileReader io.Reader) (string, error) {
	// The folder ID is caller-supplied, so it must be proven to belong to the uploader before
	// we write a file record into it.
	if folderID != nil {
		if err := s.repo.VerifyFolderOwnership(ctx, userID, *folderID); err != nil {
			return "", err
		}
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txRepo := s.repo.WithTx(tx)

	buffer := make([]byte, ChunkSize)
	n, err := io.ReadFull(fileReader, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("error reading initial file stream: %w", err)
	}

	contentType := "application/octet-stream"
	if n > 0 {
		contentType = http.DetectContentType(buffer[:n])
	}

	// Create the file record with initial 0 size and detected content type
	fileID, createErr := txRepo.CreateFile(ctx, userID, folderID, name, contentType, 0)
	if createErr != nil {
		return "", fmt.Errorf("failed to create file record: %w", createErr)
	}

	chunkIndex := 0
	var totalSize int64

	for n > 0 {
		chunkData := buffer[:n]
		totalSize += int64(n)

		// Hash the chunk for deduplication and integrity
		hashBytes := sha256.Sum256(chunkData)
		hashStr := hex.EncodeToString(hashBytes[:])

		// Atomically get or create chunk (Deduplication without TOCTOU race)
		chunkID, isNew, dbErr := txRepo.GetOrCreateChunk(ctx, hashStr, int64(n))
		if dbErr != nil {
			return "", fmt.Errorf("failed to get or create chunk: %w", dbErr)
		}

		if isNew {
			uploadErr := s.store.UploadChunk(ctx, hashStr, bytes.NewReader(chunkData), int64(n))
			if uploadErr != nil {
				return "", fmt.Errorf("failed to upload chunk to storage: %w", uploadErr)
			}
		}

		// Link chunk to file
		linkErr := txRepo.LinkFileChunk(ctx, fileID, chunkID, chunkIndex)
		if linkErr != nil {
			return "", fmt.Errorf("failed to link chunk to file: %w", linkErr)
		}

		chunkIndex++
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		n, err = io.ReadFull(fileReader, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return "", fmt.Errorf("error reading subsequent file stream: %w", err)
		}
	}

	// Update file size and status to 'ready' in DB
	err = txRepo.UpdateFileSizeAndStatus(ctx, fileID, totalSize, "ready")
	if err != nil {
		return "", fmt.Errorf("failed to update file size and status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("failed to commit upload transaction: %w", err)
	}

	// Publish an event for background workers
	if s.publisher != nil {
		_ = s.publisher.PublishFileUploaded(ctx, fileID)
	}

	return fileID, nil
}

func (s *service) DownloadFile(ctx context.Context, userID string, fileID string) (io.Reader, string, error) {
	// Verify the requesting user owns this file
	if err := s.repo.VerifyFileOwnership(ctx, userID, fileID); err != nil {
		return nil, "", fmt.Errorf("access denied: %w", err)
	}

	contentType, err := s.repo.GetFileContentType(ctx, fileID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to retrieve content type: %w", err)
	}

	chunks, err := s.repo.GetFileChunks(ctx, fileID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get file chunks: %w", err)
	}

	var readers []io.Reader
	for _, chunk := range chunks {
		reader, err := s.store.DownloadChunk(ctx, chunk.Hash)
		if err != nil {
			return nil, "", fmt.Errorf("failed to download chunk %s: %w", chunk.Hash, err)
		}
		// Wrap with on-the-fly SHA-256 integrity verification
		readers = append(readers, newIntegrityCheckingReader(reader, chunk.Hash))
	}

	// Combine all chunk streams sequentially
	return io.MultiReader(readers...), contentType, nil
}

func (s *service) DeleteFile(ctx context.Context, userID string, fileID string) error {
	err := s.repo.DeleteFile(ctx, userID, fileID)
	if err != nil {
		return fmt.Errorf("failed to delete file from database: %w", err)
	}
	return nil
}

// DownloadFileInternal is for system-level access (e.g., background workers).
// It skips ownership verification and content type retrieval since the worker only needs the raw bytes.
func (s *service) DownloadFileInternal(ctx context.Context, fileID string) (io.Reader, error) {
	chunks, err := s.repo.GetFileChunks(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to get file chunks: %w", err)
	}

	var readers []io.Reader
	for _, chunk := range chunks {
		reader, err := s.store.DownloadChunk(ctx, chunk.Hash)
		if err != nil {
			return nil, fmt.Errorf("failed to download chunk %s: %w", chunk.Hash, err)
		}
		// Wrap with integrity check
		readers = append(readers, newIntegrityCheckingReader(reader, chunk.Hash))
	}

	return io.MultiReader(readers...), nil
}

func (s *service) RunGarbageCollection(ctx context.Context, minAge time.Duration) error {
	orphaned, err := s.repo.GetOrphanedChunks(ctx, minAge)
	if err != nil {
		return fmt.Errorf("failed to query orphaned chunks: %w", err)
	}
	if len(orphaned) > 0 {
		log.Printf("Garbage collection identified %d unreferenced chunks for cleanup", len(orphaned))
	}
	for _, chunk := range orphaned {
		log.Printf("GC: Deleting orphaned chunk %s (hash: %s) from storage", chunk.ID, chunk.Hash)
		if err := s.store.DeleteChunk(ctx, chunk.Hash); err != nil {
			log.Printf("GC: Failed to delete chunk from storage: %v", err)
			continue
		}
		if err := s.repo.DeleteChunkRecord(ctx, chunk.ID); err != nil {
			log.Printf("GC: Failed to delete chunk database record: %v", err)
		}
	}
	return nil
}
