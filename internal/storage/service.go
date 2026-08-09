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
	DownloadFile(ctx context.Context, userID string, fileID string) (io.Reader, FileInfo, error)
	DownloadFileInternal(ctx context.Context, fileID string) (io.Reader, FileInfo, error)
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

	buffer := make([]byte, ChunkSize)
	n, readErr := io.ReadFull(fileReader, buffer)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("error reading initial file stream: %w", readErr)
	}

	contentType := "application/octet-stream"
	if n > 0 {
		contentType = http.DetectContentType(buffer[:n])
	}

	// The file row is committed up front as StatusUploading rather than being held in an
	// open transaction for the duration of the upload. Wrapping the whole transfer in one
	// transaction pinned a pooled connection for as long as the client took to send up to
	// 100MB and MinIO took to accept it; a handful of slow uploaders could exhaust the pool
	// and stall every other request, logins included. Only chunk writes are transactional
	// now, so connection hold time is bounded by one chunk instead of one file.
	//
	// The cost is that a failed upload leaves a row behind instead of vanishing on rollback.
	// That is why the status exists: nothing surfaces a file until it reaches StatusReady,
	// the failure path deletes the row, and RunGarbageCollection sweeps whatever a crash
	// leaves stranded.
	fileID, err := s.repo.CreateFile(ctx, userID, folderID, name, contentType, 0)
	if err != nil {
		return "", fmt.Errorf("failed to create file record: %w", err)
	}

	totalSize, err := s.storeChunks(ctx, fileID, fileReader, buffer, n, readErr)
	if err != nil {
		s.abandonUpload(ctx, userID, fileID)
		return "", err
	}

	// Publishing the file as ready is the commit point for the upload as a whole.
	if err := s.repo.UpdateFileSizeAndStatus(ctx, fileID, totalSize, StatusReady); err != nil {
		s.abandonUpload(ctx, userID, fileID)
		return "", fmt.Errorf("failed to update file size and status: %w", err)
	}

	// Publish an event for background workers
	if s.publisher != nil {
		_ = s.publisher.PublishFileUploaded(ctx, fileID)
	}

	return fileID, nil
}

// storeChunks consumes the rest of the stream, writing each chunk, and returns the total
// number of bytes stored. buffer already holds the first n bytes, read with readErr.
func (s *service) storeChunks(ctx context.Context, fileID string, fileReader io.Reader, buffer []byte, n int, readErr error) (int64, error) {
	chunkIndex := 0
	var totalSize int64

	for n > 0 {
		chunkData := buffer[:n]
		totalSize += int64(n)

		// Hash the chunk for deduplication and integrity
		hashBytes := sha256.Sum256(chunkData)
		hashStr := hex.EncodeToString(hashBytes[:])

		if err := s.storeChunk(ctx, fileID, hashStr, chunkData, chunkIndex); err != nil {
			return 0, err
		}

		chunkIndex++
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		n, readErr = io.ReadFull(fileReader, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return 0, fmt.Errorf("error reading subsequent file stream: %w", readErr)
		}
	}

	return totalSize, nil
}

// storeChunk deduplicates, uploads and links a single chunk.
//
// The transaction spans the object upload deliberately. GetOrCreateChunk takes FOR SHARE on
// an existing chunk row, and that lock is what stops the garbage collector deleting the
// backing object while we are deciding to reuse it - releasing it before the link would
// reopen the race this handshake exists to close. Holding a connection across one chunk's
// upload is the price; holding one across the entire file's upload was the bug.
func (s *service) storeChunk(ctx context.Context, fileID, hash string, data []byte, index int) error {
	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin chunk transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txRepo := s.repo.WithTx(tx)

	// Atomically get or create chunk (Deduplication without TOCTOU race)
	chunkID, isNew, err := txRepo.GetOrCreateChunk(ctx, hash, int64(len(data)))
	if err != nil {
		return fmt.Errorf("failed to get or create chunk: %w", err)
	}

	if isNew {
		if err := s.store.UploadChunk(ctx, hash, bytes.NewReader(data), int64(len(data))); err != nil {
			return fmt.Errorf("failed to upload chunk to storage: %w", err)
		}
	}

	if err := txRepo.LinkFileChunk(ctx, fileID, chunkID, index); err != nil {
		return fmt.Errorf("failed to link chunk to file: %w", err)
	}

	return tx.Commit(ctx)
}

// abandonUpload removes the record of an upload that did not finish.
//
// Best effort by design: it runs on a path where something has already gone wrong, and its
// own failure must not mask the original error. RunGarbageCollection sweeps anything left
// behind, which is also what covers the case where the process dies before reaching here.
func (s *service) abandonUpload(ctx context.Context, userID, fileID string) {
	if err := s.repo.DeleteFile(ctx, userID, fileID); err != nil {
		tracing.Logger(ctx).Error("failed to clean up abandoned upload; garbage collection will retry",
			"error", err, "file_id", fileID, "user_id", userID)
	}
}

func (s *service) DownloadFile(ctx context.Context, userID string, fileID string) (io.Reader, FileInfo, error) {
	// Verify the requesting user owns this file
	if err := s.repo.VerifyFileOwnership(ctx, userID, fileID); err != nil {
		return nil, FileInfo{}, fmt.Errorf("access denied: %w", err)
	}

	info, err := s.repo.GetFileInfo(ctx, fileID)
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("failed to retrieve file info: %w", err)
	}

	// A row that has not reached StatusReady has an incomplete chunk list, so serving it
	// would hand back a silently truncated file. Report it as absent, which is what it is
	// from the caller's point of view.
	if info.Status != StatusReady {
		return nil, FileInfo{}, ErrFileNotFound
	}

	chunks, err := s.repo.GetFileChunks(ctx, fileID)
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("failed to get file chunks: %w", err)
	}

	// Readers are lazy: each chunk is fetched and integrity-checked only when the client
	// actually reads that far, so nothing is opened for a download that is never consumed.
	readers := make([]io.Reader, 0, len(chunks))
	for _, chunk := range chunks {
		readers = append(readers, newVerifiedChunkReader(ctx, s.store, chunk.Hash))
	}

	// Combine all chunk streams sequentially
	return io.MultiReader(readers...), info, nil
}

// DeleteFile removes the file record; its chunks are reclaimed later, not here.
//
// Chunks are content-addressed and shared across files, so deleting a file cannot delete its
// chunks - another file may reference the identical content. Establishing that nothing else
// references a chunk is only sound under a lock, which is what RunGarbageCollection does.
//
// So space is reclaimed eventually rather than immediately: the worker sweeps every 6 hours
// and only collects chunks unreferenced for at least 24 hours (see cmd/worker). That age
// floor is deliberate - it keeps the collector away from in-flight uploads, whose chunk rows
// exist before the file rows that come to reference them.
func (s *service) DeleteFile(ctx context.Context, userID string, fileID string) error {
	err := s.repo.DeleteFile(ctx, userID, fileID)
	if err != nil {
		return fmt.Errorf("failed to delete file from database: %w", err)
	}
	return nil
}

// DownloadFileInternal is for system-level access (e.g., background workers).
// It skips ownership verification since workers act on behalf of the system, but still
// returns the file's metadata so callers can decide whether the content is worth reading.
func (s *service) DownloadFileInternal(ctx context.Context, fileID string) (io.Reader, FileInfo, error) {
	info, err := s.repo.GetFileInfo(ctx, fileID)
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("failed to retrieve file info: %w", err)
	}

	chunks, err := s.repo.GetFileChunks(ctx, fileID)
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("failed to get file chunks: %w", err)
	}

	readers := make([]io.Reader, 0, len(chunks))
	for _, chunk := range chunks {
		readers = append(readers, newVerifiedChunkReader(ctx, s.store, chunk.Hash))
	}

	return io.MultiReader(readers...), info, nil
}

func (s *service) RunGarbageCollection(ctx context.Context, minAge time.Duration) error {
	logger := tracing.Logger(ctx)

	// Sweep abandoned uploads first. Their file_chunks rows cascade away, which is what
	// makes the chunks they held eligible for the orphan scan below.
	if swept, err := s.repo.DeleteStaleUploads(ctx, minAge); err != nil {
		// Not fatal: chunk collection is independent and still worth running.
		logger.Error("failed to sweep stale uploads", "error", err)
	} else if swept > 0 {
		logger.Info("removed uploads abandoned before completion", "count", swept)
	}

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
