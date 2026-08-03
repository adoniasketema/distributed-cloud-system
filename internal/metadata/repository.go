package metadata

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

func (r *repository) SearchFiles(ctx context.Context, userID string, searchQuery string) ([]File, error) {
	// Optimize search utilizing GIN index on embeddings and targeted JSONB extraction
	query := `
		SELECT f.id, f.user_id, f.folder_id, f.name, f.content_type, f.size, f.status, f.created_at, f.updated_at 
		FROM files f
		LEFT JOIN file_embeddings fe ON f.id = fe.file_id
		WHERE f.user_id = $1 
		  AND (f.name ILIKE '%' || $2 || '%' 
		       OR fe.embedding @> json_build_object('tags', json_build_array($2))::jsonb
		       OR fe.embedding->>'summary' ILIKE '%' || $2 || '%'
		       OR (fe.embedding->'tags')::text ILIKE '%' || $2 || '%')
	`
	rows, err := r.db.Query(ctx, query, userID, searchQuery)
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
