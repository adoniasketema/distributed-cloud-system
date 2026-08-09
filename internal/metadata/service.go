package metadata

import (
	"context"
)

type DirectoryContent struct {
	Folders []Folder `json:"folders"`
	Files   []File   `json:"files"`
}

type Service interface {
	CreateFolder(ctx context.Context, userID string, parentID *string, name string) (*Folder, error)
	DeleteFolder(ctx context.Context, userID string, folderID string) error
	ListDirectory(ctx context.Context, userID string, folderID *string) (*DirectoryContent, error)
	SearchFiles(ctx context.Context, userID string, query string) ([]File, error)
}

type service struct {
	repo Repository
}

func NewService(repo Repository) Service {
	return &service{repo: repo}
}

func (s *service) CreateFolder(ctx context.Context, userID string, parentID *string, name string) (*Folder, error) {
	// The parent ID comes straight from the request body, so it must be proven to belong to
	// the caller. Without this check any user can graft a folder onto another user's tree.
	if parentID != nil {
		if err := s.repo.VerifyFolderOwnership(ctx, userID, *parentID); err != nil {
			return nil, err
		}
	}
	return s.repo.CreateFolder(ctx, userID, parentID, name)
}

// DeleteFolder removes a folder and everything beneath it.
//
// The recursion is done by PostgreSQL, not here: folders.parent_id, files.folder_id and
// file_chunks.file_id all declare ON DELETE CASCADE (migrations 00002 and 00003), so one
// DELETE removes the whole subtree. Chunk rows survive - file_chunks.chunk_id is RESTRICT
// and chunks are shared between files - and are reclaimed later by the storage garbage
// collector once nothing references them.
func (s *service) DeleteFolder(ctx context.Context, userID string, folderID string) error {
	return s.repo.DeleteFolder(ctx, userID, folderID)
}

func (s *service) ListDirectory(ctx context.Context, userID string, folderID *string) (*DirectoryContent, error) {
	folders, err := s.repo.ListFolders(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}

	files, err := s.repo.ListFiles(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}

	if folders == nil {
		folders = []Folder{}
	}
	if files == nil {
		files = []File{}
	}

	return &DirectoryContent{
		Folders: folders,
		Files:   files,
	}, nil
}

func (s *service) SearchFiles(ctx context.Context, userID string, query string) ([]File, error) {
	if query == "" {
		return []File{}, nil
	}
	files, err := s.repo.SearchFiles(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	if files == nil {
		return []File{}, nil
	}
	return files, nil
}
