package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrFileNotFound = errors.New("file not found")
	ErrAccessDenied = errors.New("file not found or access denied")
	// ErrChunkInUse means a chunk gained a reference since it was listed as orphaned,
	// so the garbage collector must leave it alone.
	ErrChunkInUse = errors.New("chunk is no longer orphaned")
)

type Chunk struct {
	ID   string
	Hash string
	Size int64
}

// DBTX is an interface that both *pgxpool.Pool and pgx.Tx satisfy, allowing the same
// repository methods to work with or without transactions.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Repository interface {
	// Transaction support
	BeginTx(ctx context.Context) (pgx.Tx, error)
	WithTx(tx pgx.Tx) Repository

	CreateFile(ctx context.Context, userID string, folderID *string, name string, contentType string, size int64) (string, error)
	GetFileContentType(ctx context.Context, fileID string) (string, error)
	DeleteFile(ctx context.Context, userID string, fileID string) error
	VerifyFileOwnership(ctx context.Context, userID string, fileID string) error
	VerifyFolderOwnership(ctx context.Context, userID string, folderID string) error
	UpdateFileSizeAndStatus(ctx context.Context, fileID string, size int64, status string) error
	GetOrCreateChunk(ctx context.Context, hash string, size int64) (string, bool, error)
	LinkFileChunk(ctx context.Context, fileID, chunkID string, index int) error
	GetFileChunks(ctx context.Context, fileID string) ([]Chunk, error)

	// Garbage collection support
	GetOrphanedChunks(ctx context.Context, minAge time.Duration) ([]Chunk, error)
	LockOrphanedChunk(ctx context.Context, chunkID string) error
	DeleteChunkRecord(ctx context.Context, chunkID string) error
}

type repository struct {
	db   DBTX
	pool *pgxpool.Pool // kept only for BeginTx
}

func NewRepository(db *pgxpool.Pool) Repository {
	return &repository{db: db, pool: db}
}

func (r *repository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

// WithTx returns a copy of the repository that runs queries within the given transaction.
func (r *repository) WithTx(tx pgx.Tx) Repository {
	return &repository{db: tx, pool: r.pool}
}

func (r *repository) CreateFile(ctx context.Context, userID string, folderID *string, name string, contentType string, size int64) (string, error) {
	query := `
		INSERT INTO files (user_id, folder_id, name, content_type, size, status)
		VALUES ($1, $2, $3, $4, $5, 'uploading')
		RETURNING id
	`
	var fileID string
	err := r.db.QueryRow(ctx, query, userID, folderID, name, contentType, size).Scan(&fileID)
	if err != nil {
		return "", err
	}
	return fileID, nil
}

func (r *repository) GetFileContentType(ctx context.Context, fileID string) (string, error) {
	query := `SELECT content_type FROM files WHERE id = $1`
	var contentType string
	err := r.db.QueryRow(ctx, query, fileID).Scan(&contentType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrFileNotFound
		}
		return "", err
	}
	return contentType, nil
}

func (r *repository) UpdateFileSizeAndStatus(ctx context.Context, fileID string, size int64, status string) error {
	query := `
		UPDATE files
		SET size = $1, status = $2, updated_at = NOW()
		WHERE id = $3
	`
	_, err := r.db.Exec(ctx, query, size, status, fileID)
	return err
}

func (r *repository) DeleteFile(ctx context.Context, userID string, fileID string) error {
	query := `DELETE FROM files WHERE id = $1 AND user_id = $2`
	result, err := r.db.Exec(ctx, query, fileID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrAccessDenied
	}
	return nil
}

func (r *repository) VerifyFileOwnership(ctx context.Context, userID string, fileID string) error {
	query := `SELECT 1 FROM files WHERE id = $1 AND user_id = $2`
	var exists int
	err := r.db.QueryRow(ctx, query, fileID, userID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccessDenied
		}
		return err
	}
	return nil
}

// VerifyFolderOwnership returns ErrAccessDenied unless the folder exists and belongs to userID.
// A malformed folder ID is reported as ErrAccessDenied rather than a database error.
func (r *repository) VerifyFolderOwnership(ctx context.Context, userID string, folderID string) error {
	query := `SELECT 1 FROM folders WHERE id = $1 AND user_id = $2`
	var exists int
	err := r.db.QueryRow(ctx, query, folderID, userID).Scan(&exists)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "22P02") {
			return ErrAccessDenied
		}
		return err
	}
	return nil
}

// GetOrCreateChunk atomically inserts a chunk or returns the existing one.
// It returns (chunkID, isNew, error), eliminating the TOCTOU race condition in deduplication.
func (r *repository) GetOrCreateChunk(ctx context.Context, hash string, size int64) (string, bool, error) {
	const insertQuery = `
		INSERT INTO chunks (hash, size)
		VALUES ($1, $2)
		ON CONFLICT (hash) DO NOTHING
		RETURNING id
	`
	// FOR SHARE, not a plain SELECT. If the garbage collector is mid-flight for this hash it
	// holds FOR UPDATE on the row while it deletes the backing object, so we must wait it
	// out: a plain SELECT would hand back the doomed row, we would treat the chunk as already
	// stored, skip the upload, and leave this file pointing at an object about to vanish.
	// Once GC commits, the row is gone and we fall through to a re-insert that re-uploads.
	//
	// A shared lock (rather than FOR UPDATE) lets concurrent uploads referencing the same
	// chunks in different orders proceed without deadlocking each other, while still being
	// enough to block GC.
	const selectQuery = `SELECT id FROM chunks WHERE hash = $1 FOR SHARE`

	// Insert and select can each lose a race with a concurrent writer; retrying converges.
	for attempt := 0; attempt < 3; attempt++ {
		var chunkID string
		err := r.db.QueryRow(ctx, insertQuery, hash, size).Scan(&chunkID)
		if err == nil {
			return chunkID, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}

		err = r.db.QueryRow(ctx, selectQuery, hash).Scan(&chunkID)
		if err == nil {
			return chunkID, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
		// The row was collected between our insert and our select. Retry the insert.
	}
	return "", false, fmt.Errorf("chunk %s: could not resolve after repeated races with concurrent writers", hash)
}

// LockOrphanedChunk takes an exclusive row lock on a chunk, but only while it is still
// unreferenced. It returns ErrChunkInUse if the chunk gained a reference (or was already
// collected) since it was listed, which makes it safe to call on a stale scan result.
//
// The lock is held until the caller's transaction ends, which is what lets the garbage
// collector delete the backing object and the row as one unit with respect to uploads.
func (r *repository) LockOrphanedChunk(ctx context.Context, chunkID string) error {
	query := `
		SELECT 1
		FROM chunks c
		WHERE c.id = $1
		  AND NOT EXISTS (SELECT 1 FROM file_chunks fc WHERE fc.chunk_id = c.id)
		FOR UPDATE
	`
	var exists int
	err := r.db.QueryRow(ctx, query, chunkID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChunkInUse
		}
		return err
	}
	return nil
}

func (r *repository) LinkFileChunk(ctx context.Context, fileID, chunkID string, index int) error {
	query := `
		INSERT INTO file_chunks (file_id, chunk_id, chunk_index)
		VALUES ($1, $2, $3)
	`
	_, err := r.db.Exec(ctx, query, fileID, chunkID, index)
	return err
}

func (r *repository) GetFileChunks(ctx context.Context, fileID string) ([]Chunk, error) {
	query := `
		SELECT c.id, c.hash, c.size 
		FROM chunks c
		JOIN file_chunks fc ON c.id = fc.chunk_id
		WHERE fc.file_id = $1
		ORDER BY fc.chunk_index ASC
	`
	rows, err := r.db.Query(ctx, query, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.Hash, &c.Size); err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, nil
}

func (r *repository) GetOrphanedChunks(ctx context.Context, minAge time.Duration) ([]Chunk, error) {
	query := `
		SELECT c.id, c.hash, c.size
		FROM chunks c
		LEFT JOIN file_chunks fc ON c.id = fc.chunk_id
		WHERE fc.chunk_id IS NULL AND c.created_at < $1
	`
	cutoff := time.Now().Add(-minAge)
	rows, err := r.db.Query(ctx, query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.Hash, &c.Size); err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, nil
}

func (r *repository) DeleteChunkRecord(ctx context.Context, chunkID string) error {
	query := `DELETE FROM chunks WHERE id = $1`
	_, err := r.db.Exec(ctx, query, chunkID)
	return err
}
