package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrFileNotFound = errors.New("file not found")
	ErrAccessDenied = errors.New("file not found or access denied")
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
	insertQuery := `
		INSERT INTO chunks (hash, size)
		VALUES ($1, $2)
		ON CONFLICT (hash) DO NOTHING
		RETURNING id
	`
	var chunkID string
	err := r.db.QueryRow(ctx, insertQuery, hash, size).Scan(&chunkID)
	if err == nil {
		return chunkID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}

	selectQuery := `SELECT id FROM chunks WHERE hash = $1`
	err = r.db.QueryRow(ctx, selectQuery, hash).Scan(&chunkID)
	if err != nil {
		return "", false, err
	}
	return chunkID, false, nil
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
