package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"nimbus/internal/events"
	"nimbus/internal/tracing"
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

// verifiedChunkReader streams a single content-addressed chunk.
//
// It fetches the chunk on first Read (not at construction time), so building a reader for an
// N-chunk file opens exactly one object handle at a time instead of N — and that handle is
// closed before any bytes are handed back, so an abandoned download leaks nothing.
//
// The whole chunk is buffered and its SHA-256 checked against the expected hash *before* any
// byte is returned. Verifying up front is what makes the check useful: a streaming hash can
// only report corruption after the caller has already received and written out the bad data.
// Chunks are bounded at ChunkSize, so this costs at most one chunk of memory per active read.
type verifiedChunkReader struct {
	store    ObjectStore
	ctx      context.Context
	expected string
	buf      *bytes.Reader
	err      error
}

func newVerifiedChunkReader(ctx context.Context, store ObjectStore, expectedHash string) io.Reader {
	return &verifiedChunkReader{ctx: ctx, store: store, expected: expectedHash}
}

func (v *verifiedChunkReader) Read(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	if v.buf == nil {
		if err := v.load(); err != nil {
			v.err = err
			return 0, err
		}
	}
	return v.buf.Read(p)
}

func (v *verifiedChunkReader) load() error {
	rc, err := v.store.DownloadChunk(v.ctx, v.expected)
	if err != nil {
		return fmt.Errorf("failed to download chunk %s: %w", v.expected, err)
	}
	defer rc.Close()

	// Read one byte past the limit so an oversized object is detected rather than truncated.
	data, err := io.ReadAll(io.LimitReader(rc, ChunkSize+1))
	if err != nil {
		return fmt.Errorf("failed to read chunk %s: %w", v.expected, err)
	}
	if len(data) > ChunkSize {
		return fmt.Errorf("chunk %s exceeds maximum chunk size", v.expected)
	}

	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != v.expected {
		return fmt.Errorf("chunk integrity check failed: expected hash %s got %s", v.expected, got)
	}

	v.buf = bytes.NewReader(data)
	return nil
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

	// Readers are lazy: each chunk is fetched and integrity-checked only when the client
	// actually reads that far, so nothing is opened for a download that is never consumed.
	readers := make([]io.Reader, 0, len(chunks))
	for _, chunk := range chunks {
		readers = append(readers, newVerifiedChunkReader(ctx, s.store, chunk.Hash))
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

	readers := make([]io.Reader, 0, len(chunks))
	for _, chunk := range chunks {
		readers = append(readers, newVerifiedChunkReader(ctx, s.store, chunk.Hash))
	}

	return io.MultiReader(readers...), nil
}

func (s *service) RunGarbageCollection(ctx context.Context, minAge time.Duration) error {
	logger := tracing.Logger(ctx)

	orphaned, err := s.repo.GetOrphanedChunks(ctx, minAge)
	if err != nil {
		return fmt.Errorf("failed to query orphaned chunks: %w", err)
	}
	if len(orphaned) == 0 {
		return nil
	}
	logger.Info("garbage collection identified unreferenced chunks", "count", len(orphaned))

	var collected, skipped int
	for _, chunk := range orphaned {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch err := s.collectChunk(ctx, chunk); {
		case err == nil:
			collected++
		case errors.Is(err, ErrChunkInUse):
			// Referenced by an upload that landed after the scan; correct to leave it.
			skipped++
		default:
			logger.Error("failed to collect orphaned chunk", "error", err, "chunk_id", chunk.ID, "hash", chunk.Hash)
		}
	}

	logger.Info("garbage collection finished", "collected", collected, "skipped", skipped, "candidates", len(orphaned))
	return nil
}

// collectChunk removes one orphaned chunk and its backing object.
//
// The row is locked and re-checked for references inside a transaction, and the object is
// deleted *before* that transaction commits. Holding the lock across the object delete is
// what makes this safe against a concurrent upload of identical content: such an upload
// blocks on the row (see GetOrCreateChunk), and by the time it proceeds the row is gone, so
// it re-inserts and re-uploads rather than adopting a chunk whose object we just removed.
//
// Deleting the object first also means a crash mid-collection leaves a row pointing at a
// missing object only if the commit succeeds, which it cannot once we have rolled back.
func (s *service) collectChunk(ctx context.Context, chunk Chunk) error {
	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin gc transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txRepo := s.repo.WithTx(tx)

	if err := txRepo.LockOrphanedChunk(ctx, chunk.ID); err != nil {
		return err
	}

	if err := s.store.DeleteChunk(ctx, chunk.Hash); err != nil {
		return fmt.Errorf("failed to delete chunk object from storage: %w", err)
	}

	if err := txRepo.DeleteChunkRecord(ctx, chunk.ID); err != nil {
		return fmt.Errorf("failed to delete chunk record: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit chunk collection: %w", err)
	}
	return nil
}
