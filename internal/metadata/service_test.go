package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockMetadataRepository is a mock implementation of Repository
type MockMetadataRepository struct {
	mock.Mock
}

func (m *MockMetadataRepository) CreateFolder(ctx context.Context, userID string, parentID *string, name string) (*Folder, error) {
	args := m.Called(ctx, userID, parentID, name)
	if folder := args.Get(0); folder != nil {
		return folder.(*Folder), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockMetadataRepository) ListFolders(ctx context.Context, userID string, parentID *string) ([]Folder, error) {
	args := m.Called(ctx, userID, parentID)
	if folders := args.Get(0); folders != nil {
		return folders.([]Folder), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockMetadataRepository) ListFiles(ctx context.Context, userID string, folderID *string) ([]File, error) {
	args := m.Called(ctx, userID, folderID)
	if files := args.Get(0); files != nil {
		return files.([]File), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockMetadataRepository) DeleteFolder(ctx context.Context, userID string, folderID string) error {
	args := m.Called(ctx, userID, folderID)
	return args.Error(0)
}

func (m *MockMetadataRepository) VerifyFolderOwnership(ctx context.Context, userID string, folderID string) error {
	args := m.Called(ctx, userID, folderID)
	return args.Error(0)
}

func (m *MockMetadataRepository) SearchFiles(ctx context.Context, userID string, searchQuery string) ([]File, error) {
	args := m.Called(ctx, userID, searchQuery)
	if files := args.Get(0); files != nil {
		return files.([]File), args.Error(1)
	}
	return nil, args.Error(1)
}

func TestMetadataService_CreateFolder(t *testing.T) {
	mockRepo := new(MockMetadataRepository)
	svc := NewService(mockRepo)

	userID := "user-123"
	folderName := "Documents"
	
	expectedFolder := &Folder{
		ID:        "folder-123",
		UserID:    userID,
		ParentID:  nil,
		Name:      folderName,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	mockRepo.On("CreateFolder", mock.Anything, userID, (*string)(nil), folderName).Return(expectedFolder, nil)

	folder, err := svc.CreateFolder(context.Background(), userID, nil, folderName)

	assert.NoError(t, err)
	assert.NotNil(t, folder)
	assert.Equal(t, "folder-123", folder.ID)
	assert.Equal(t, folderName, folder.Name)
	mockRepo.AssertExpectations(t)
}

func TestMetadataService_CreateFolder_VerifiesParentOwnership(t *testing.T) {
	mockRepo := new(MockMetadataRepository)
	svc := NewService(mockRepo)

	userID := "user-123"
	parentID := "folder-owned-by-user-123"

	mockRepo.On("VerifyFolderOwnership", mock.Anything, userID, parentID).Return(nil)
	mockRepo.On("CreateFolder", mock.Anything, userID, &parentID, "Reports").
		Return(&Folder{ID: "folder-new", UserID: userID, ParentID: &parentID, Name: "Reports"}, nil)

	folder, err := svc.CreateFolder(context.Background(), userID, &parentID, "Reports")

	assert.NoError(t, err)
	assert.Equal(t, "folder-new", folder.ID)
	mockRepo.AssertExpectations(t)
}

func TestMetadataService_CreateFolder_RejectsForeignParent(t *testing.T) {
	mockRepo := new(MockMetadataRepository)
	svc := NewService(mockRepo)

	attacker := "user-attacker"
	victimFolder := "folder-owned-by-victim"

	mockRepo.On("VerifyFolderOwnership", mock.Anything, attacker, victimFolder).Return(ErrNotFound)

	folder, err := svc.CreateFolder(context.Background(), attacker, &victimFolder, "pwned")

	assert.ErrorIs(t, err, ErrNotFound)
	assert.Nil(t, folder)
	// The insert must never be attempted for a folder the caller does not own.
	mockRepo.AssertNotCalled(t, "CreateFolder", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockRepo.AssertExpectations(t)
}

func TestMetadataService_ListDirectory(t *testing.T) {
	mockRepo := new(MockMetadataRepository)
	svc := NewService(mockRepo)

	userID := "user-123"

	mockFolders := []Folder{
		{ID: "folder-1", UserID: userID, Name: "SubFolder1"},
	}
	mockFiles := []File{
		{ID: "file-1", UserID: userID, Name: "doc.txt", Size: 1024},
	}

	mockRepo.On("ListFolders", mock.Anything, userID, (*string)(nil)).Return(mockFolders, nil)
	mockRepo.On("ListFiles", mock.Anything, userID, (*string)(nil)).Return(mockFiles, nil)

	content, err := svc.ListDirectory(context.Background(), userID, nil)

	assert.NoError(t, err)
	assert.NotNil(t, content)
	assert.Len(t, content.Folders, 1)
	assert.Len(t, content.Files, 1)
	assert.Equal(t, "SubFolder1", content.Folders[0].Name)
	assert.Equal(t, "doc.txt", content.Files[0].Name)
	mockRepo.AssertExpectations(t)
}
