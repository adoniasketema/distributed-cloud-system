package metadata

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound      = errors.New("folder not found or access denied")
	ErrDuplicateName = errors.New("duplicate name in directory")
)

type Folder struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ParentID  *string   `json:"parent_id"` // Nullable
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type File struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	FolderID    *string   `json:"folder_id"` // Nullable
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Repository interface {
	CreateFolder(ctx context.Context, userID string, parentID *string, name string) (*Folder, error)
	ListFolders(ctx context.Context, userID string, parentID *string) ([]Folder, error)
	DeleteFolder(ctx context.Context, userID string, folderID string) error
	VerifyFolderOwnership(ctx context.Context, userID string, folderID string) error
	ListFiles(ctx context.Context, userID string, folderID *string) ([]File, error)
	SearchFiles(ctx context.Context, userID string, searchQuery string) ([]File, error)
}

type repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) Repository {
	return &repository{db: db}
}

func (r *repository) CreateFolder(ctx context.Context, userID string, parentID *string, name string) (*Folder, error) {
	query := `
		INSERT INTO folders (user_id, parent_id, name)
		VALUES ($1, $2, $3)
		RETURNING id, user_id, parent_id, name, created_at, updated_at
	`

	var folder Folder
	err := r.db.QueryRow(ctx, query, userID, parentID, name).Scan(
		&folder.ID,
		&folder.UserID,
		&folder.ParentID,
		&folder.Name,
		&folder.CreatedAt,
		&folder.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique constraint") {
			return nil, ErrDuplicateName
		}
		return nil, err
	}
	return &folder, nil
}

func (r *repository) ListFolders(ctx context.Context, userID string, parentID *string) ([]Folder, error) {
	var query string
	var rows pgx.Rows
	var err error

	if parentID == nil {
		query = `SELECT id, user_id, parent_id, name, created_at, updated_at FROM folders WHERE user_id = $1 AND parent_id IS NULL`
		rows, err = r.db.Query(ctx, query, userID)
	} else {
		query = `SELECT id, user_id, parent_id, name, created_at, updated_at FROM folders WHERE user_id = $1 AND parent_id = $2`
		rows, err = r.db.Query(ctx, query, userID, *parentID)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.UserID, &f.ParentID, &f.Name, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, nil
}

func (r *repository) DeleteFolder(ctx context.Context, userID string, folderID string) error {
	query := `DELETE FROM folders WHERE id = $1 AND user_id = $2`
	result, err := r.db.Exec(ctx, query, folderID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// VerifyFolderOwnership returns ErrNotFound unless the folder exists and belongs to userID.
// A malformed folder ID is reported as ErrNotFound rather than a database error, so callers
// cannot distinguish "bad id" from "someone else's folder".
func (r *repository) VerifyFolderOwnership(ctx context.Context, userID string, folderID string) error {
	query := `SELECT 1 FROM folders WHERE id = $1 AND user_id = $2`
	var exists int
	err := r.db.QueryRow(ctx, query, folderID, userID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// isInvalidTextRepresentation reports whether err is PostgreSQL's 22P02, which is what
// the server returns when a non-UUID string is compared against a UUID column.
func isInvalidTextRepresentation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

func (r *repository) ListFiles(ctx context.Context, userID string, folderID *string) ([]File, error) {
	var query string
	var rows pgx.Rows
	var err error

	if folderID == nil {
		query = `SELECT id, user_id, folder_id, name, content_type, size, status, created_at, updated_at FROM files WHERE user_id = $1 AND folder_id IS NULL`
		rows, err = r.db.Query(ctx, query, userID)
	} else {
		query = `SELECT id, user_id, folder_id, name, content_type, size, status, created_at, updated_at FROM files WHERE user_id = $1 AND folder_id = $2`
		rows, err = r.db.Query(ctx, query, userID, *folderID)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.UserID, &f.FolderID, &f.Name, &f.ContentType, &f.Size, &f.Status, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// MaxSearchResults bounds a single search response. Search has no pagination, so without a
// cap a common substring returns every file the user owns in one unbounded result set.
const MaxSearchResults = 200

// likePatternEscaper neutralises ILIKE wildcards in user input so a search for "%" matches
// a literal percent sign instead of every row. Backslash is PostgreSQL's default LIKE
// escape character, so it has to be escaped first for the other rules to hold.
var likePatternEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (r *repository) SearchFiles(ctx context.Context, userID string, searchQuery string) ([]File, error) {
	// Optimize search utilizing GIN index on embeddings and targeted JSONB extraction.
	// $2 is the raw term, used for exact JSONB tag containment. $3 is the same term with
	// ILIKE metacharacters escaped, used for every substring match.
	query := `
		SELECT f.id, f.user_id, f.folder_id, f.name, f.content_type, f.size, f.status, f.created_at, f.updated_at
		FROM files f
		LEFT JOIN file_embeddings fe ON f.id = fe.file_id
		WHERE f.user_id = $1
		  AND (f.name ILIKE '%' || $3 || '%'
		       OR fe.embedding @> json_build_object('tags', json_build_array($2))::jsonb
		       OR fe.embedding->>'summary' ILIKE '%' || $3 || '%'
		       OR (fe.embedding->'tags')::text ILIKE '%' || $3 || '%')
		ORDER BY f.created_at DESC, f.id
		LIMIT $4
	`
	rows, err := r.db.Query(ctx, query, userID, searchQuery, likePatternEscaper.Replace(searchQuery), MaxSearchResults)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.UserID, &f.FolderID, &f.Name, &f.ContentType, &f.Size, &f.Status, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}
