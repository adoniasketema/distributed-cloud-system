package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// mockTx implements pgx.Tx by embedding it and overriding needed methods
type mockTx struct {
	pgx.Tx
	mock.Mock
}

func (m *mockTx) Commit(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockTx) Rollback(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockRepository is a mock implementation of Repository
type MockRepository struct {
	mock.Mock
}

func (m *MockRepository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	args := m.Called(ctx)
	if tx := args.Get(0); tx != nil {
		return tx.(pgx.Tx), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockRepository) WithTx(tx pgx.Tx) Repository {
	args := m.Called(tx)
	return args.Get(0).(Repository)
}

func (m *MockRepository) CreateFile(ctx context.Context, userID string, folderID *string, name string, contentType string, size int64) (string, error) {
	args := m.Called(ctx, userID, folderID, name, contentType, size)
	return args.String(0), args.Error(1)
}

func (m *MockRepository) GetFileInfo(ctx context.Context, fileID string) (FileInfo, error) {
	args := m.Called(ctx, fileID)
	return args.Get(0).(FileInfo), args.Error(1)
}

func (m *MockRepository) DeleteFile(ctx context.Context, userID string, fileID string) error {
	args := m.Called(ctx, userID, fileID)
	return args.Error(0)
}

func (m *MockRepository) VerifyFileOwnership(ctx context.Context, userID string, fileID string) error {
	args := m.Called(ctx, userID, fileID)
	return args.Error(0)
}

func (m *MockRepository) VerifyFolderOwnership(ctx context.Context, userID string, folderID string) error {
	args := m.Called(ctx, userID, folderID)
	return args.Error(0)
}

func (m *MockRepository) UpdateFileSizeAndStatus(ctx context.Context, fileID string, size int64, status string) error {
	args := m.Called(ctx, fileID, size, status)
	return args.Error(0)
}

func (m *MockRepository) GetOrCreateChunk(ctx context.Context, hash string, size int64) (string, bool, error) {
	args := m.Called(ctx, hash, size)
	return args.String(0), args.Bool(1), args.Error(2)
}

func (m *MockRepository) LinkFileChunk(ctx context.Context, fileID, chunkID string, index int) error {
	args := m.Called(ctx, fileID, chunkID, index)
	return args.Error(0)
}

func (m *MockRepository) GetFileChunks(ctx context.Context, fileID string) ([]Chunk, error) {
	args := m.Called(ctx, fileID)
	if chunks := args.Get(0); chunks != nil {
		return chunks.([]Chunk), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockRepository) GetOrphanedChunks(ctx context.Context, minAge time.Duration) ([]Chunk, error) {
	args := m.Called(ctx, minAge)
	if chunks := args.Get(0); chunks != nil {
		return chunks.([]Chunk), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockRepository) LockOrphanedChunk(ctx context.Context, chunkID string) error {
	args := m.Called(ctx, chunkID)
	return args.Error(0)
}

func (m *MockRepository) DeleteChunkRecord(ctx context.Context, chunkID string) error {
	args := m.Called(ctx, chunkID)
	return args.Error(0)
}

// MockObjectStore is a mock implementation of ObjectStore
type MockObjectStore struct {
	mock.Mock
}

func (m *MockObjectStore) UploadChunk(ctx context.Context, hash string, reader io.Reader, size int64) error {
	args := m.Called(ctx, hash, reader, size)
	return args.Error(0)
}

func (m *MockObjectStore) DownloadChunk(ctx context.Context, hash string) (io.ReadCloser, error) {
	args := m.Called(ctx, hash)
	if rc := args.Get(0); rc != nil {
		return rc.(io.ReadCloser), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockObjectStore) DeleteChunk(ctx context.Context, hash string) error {
	args := m.Called(ctx, hash)
	return args.Error(0)
}

func TestStorageService_UploadFile_NewChunks(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	userID := "user-123"
	fileName := "test.txt"
	fileID := "file-123"

	// Prepare dummy data: 6MB of zeroes
	data := make([]byte, 6*1024*1024)
	reader := bytes.NewReader(data)

	// We expect 2 chunks. One of 4MB, one of 2MB
	chunk1Data := make([]byte, 4*1024*1024)
	chunk2Data := make([]byte, 2*1024*1024)

	hash1Bytes := sha256.Sum256(chunk1Data)
	hash1Str := hex.EncodeToString(hash1Bytes[:])

	hash2Bytes := sha256.Sum256(chunk2Data)
	hash2Str := hex.EncodeToString(hash2Bytes[:])

	mockTx := new(mockTx)
	mockTx.On("Commit", mock.Anything).Return(nil)
	mockTx.On("Rollback", mock.Anything).Return(nil)

	mockRepo.On("BeginTx", mock.Anything).Return(mockTx, nil)
	mockRepo.On("WithTx", mockTx).Return(mockRepo)
	mockRepo.On("CreateFile", mock.Anything, userID, (*string)(nil), fileName, mock.AnythingOfType("string"), int64(0)).Return(fileID, nil)

	// Chunk 1 expectations (isNew = true)
	mockRepo.On("GetOrCreateChunk", mock.Anything, hash1Str, int64(4*1024*1024)).Return("chunk-1", true, nil)
	mockStore.On("UploadChunk", mock.Anything, hash1Str, mock.Anything, int64(4*1024*1024)).Return(nil)
	mockRepo.On("LinkFileChunk", mock.Anything, fileID, "chunk-1", 0).Return(nil)

	// Chunk 2 expectations (isNew = true)
	mockRepo.On("GetOrCreateChunk", mock.Anything, hash2Str, int64(2*1024*1024)).Return("chunk-2", true, nil)
	mockStore.On("UploadChunk", mock.Anything, hash2Str, mock.Anything, int64(2*1024*1024)).Return(nil)
	mockRepo.On("LinkFileChunk", mock.Anything, fileID, "chunk-2", 1).Return(nil)

	mockRepo.On("UpdateFileSizeAndStatus", mock.Anything, fileID, int64(6*1024*1024), "ready").Return(nil)

	id, err := svc.UploadFile(context.Background(), userID, nil, fileName, reader)

	assert.NoError(t, err)
	assert.Equal(t, fileID, id)
	mockRepo.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockTx.AssertExpectations(t)
}

func TestStorageService_UploadFile_Deduplication(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	userID := "user-123"
	fileName := "test.txt"
	fileID := "file-123"

	// Prepare dummy data: 4MB of zeroes
	data := make([]byte, 4*1024*1024)
	reader := bytes.NewReader(data)

	hashBytes := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hashBytes[:])

	mockTx := new(mockTx)
	mockTx.On("Commit", mock.Anything).Return(nil)
	mockTx.On("Rollback", mock.Anything).Return(nil)

	mockRepo.On("BeginTx", mock.Anything).Return(mockTx, nil)
	mockRepo.On("WithTx", mockTx).Return(mockRepo)
	mockRepo.On("CreateFile", mock.Anything, userID, (*string)(nil), fileName, mock.AnythingOfType("string"), int64(0)).Return(fileID, nil)

	// Chunk exists! (isNew = false)
	mockRepo.On("GetOrCreateChunk", mock.Anything, hashStr, int64(4*1024*1024)).Return("chunk-existing", false, nil)
	// Notice: UploadChunk is NOT called because of deduplication
	mockRepo.On("LinkFileChunk", mock.Anything, fileID, "chunk-existing", 0).Return(nil)

	mockRepo.On("UpdateFileSizeAndStatus", mock.Anything, fileID, int64(4*1024*1024), "ready").Return(nil)

	id, err := svc.UploadFile(context.Background(), userID, nil, fileName, reader)

	assert.NoError(t, err)
	assert.Equal(t, fileID, id)
	mockRepo.AssertExpectations(t)
	mockStore.AssertExpectations(t) // Ensure no upload was made to storage
	mockTx.AssertExpectations(t)
}

func TestStorageService_UploadFile_RejectsForeignFolder(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	attacker := "user-attacker"
	victimFolder := "folder-owned-by-victim"

	mockRepo.On("VerifyFolderOwnership", mock.Anything, attacker, victimFolder).Return(ErrAccessDenied)

	id, err := svc.UploadFile(context.Background(), attacker, &victimFolder, "pwned.txt", bytes.NewReader([]byte("x")))

	assert.ErrorIs(t, err, ErrAccessDenied)
	assert.Empty(t, id)
	// No transaction should even be opened for a folder the caller does not own.
	mockRepo.AssertNotCalled(t, "BeginTx", mock.Anything)
	mockRepo.AssertExpectations(t)
}

func TestStorageService_DownloadFile(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	userID := "user-123"
	fileID := "file-123"
	content := []byte("content")
	hashBytes := sha256.Sum256(content)
	hashStr := hex.EncodeToString(hashBytes[:])

	mockRepo.On("VerifyFileOwnership", mock.Anything, userID, fileID).Return(nil)
	mockRepo.On("GetFileInfo", mock.Anything, fileID).Return(FileInfo{Name: "doc.txt", ContentType: "text/plain"}, nil)
	mockRepo.On("GetFileChunks", mock.Anything, fileID).Return([]Chunk{
		{ID: "c1", Hash: hashStr, Size: int64(len(content))},
	}, nil)

	mockStore.On("DownloadChunk", mock.Anything, hashStr).Return(io.NopCloser(bytes.NewReader(content)), nil)

	reader, info, err := svc.DownloadFile(context.Background(), userID, fileID)
	assert.NoError(t, err)
	assert.Equal(t, "text/plain", info.ContentType)
	assert.Equal(t, "doc.txt", info.Name)

	readContent, err := io.ReadAll(reader)
	assert.NoError(t, err)
	assert.Equal(t, string(content), string(readContent))
}

// trackedCloser records whether Close was called, so we can assert object handles are
// not leaked when a download is abandoned or fails verification.
type trackedCloser struct {
	io.Reader
	closed bool
}

func (t *trackedCloser) Close() error {
	t.closed = true
	return nil
}

func TestStorageService_DownloadFile_RejectsCorruptChunkBeforeYieldingData(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	userID := "user-123"
	fileID := "file-123"

	// The stored bytes do not match the hash the chunk is addressed by.
	expected := sha256.Sum256([]byte("original content"))
	expectedHash := hex.EncodeToString(expected[:])
	corrupted := &trackedCloser{Reader: bytes.NewReader([]byte("tampered content"))}

	mockRepo.On("VerifyFileOwnership", mock.Anything, userID, fileID).Return(nil)
	mockRepo.On("GetFileInfo", mock.Anything, fileID).Return(FileInfo{Name: "doc.txt", ContentType: "text/plain"}, nil)
	mockRepo.On("GetFileChunks", mock.Anything, fileID).Return([]Chunk{
		{ID: "c1", Hash: expectedHash, Size: 16},
	}, nil)
	mockStore.On("DownloadChunk", mock.Anything, expectedHash).Return(corrupted, nil)

	reader, _, err := svc.DownloadFile(context.Background(), userID, fileID)
	assert.NoError(t, err)

	got, err := io.ReadAll(reader)
	assert.Error(t, err, "corrupt chunk must surface an error")
	assert.Contains(t, err.Error(), "integrity check failed")
	// The critical property: not one tampered byte was handed to the caller.
	assert.Empty(t, got)
	assert.True(t, corrupted.closed, "chunk handle must be closed")
}

func TestStorageService_DownloadFile_DoesNotOpenChunksUntilRead(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	userID := "user-123"
	fileID := "file-123"

	mockRepo.On("VerifyFileOwnership", mock.Anything, userID, fileID).Return(nil)
	mockRepo.On("GetFileInfo", mock.Anything, fileID).Return(FileInfo{Name: "doc.txt", ContentType: "text/plain"}, nil)
	mockRepo.On("GetFileChunks", mock.Anything, fileID).Return([]Chunk{
		{ID: "c1", Hash: "hash-1", Size: 4},
		{ID: "c2", Hash: "hash-2", Size: 4},
		{ID: "c3", Hash: "hash-3", Size: 4},
	}, nil)

	_, _, err := svc.DownloadFile(context.Background(), userID, fileID)
	assert.NoError(t, err)

	// A download that is built but never read must not have opened a single object handle.
	mockStore.AssertNotCalled(t, "DownloadChunk", mock.Anything, mock.Anything)
}

func TestStorageService_RunGarbageCollection(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	orphan := Chunk{ID: "chunk-999", Hash: "deadbeefhash", Size: 4096}
	minAge := 10 * time.Minute

	mockTx := new(mockTx)
	mockTx.On("Commit", mock.Anything).Return(nil)
	mockTx.On("Rollback", mock.Anything).Return(nil)

	mockRepo.On("GetOrphanedChunks", mock.Anything, minAge).Return([]Chunk{orphan}, nil)
	mockRepo.On("BeginTx", mock.Anything).Return(mockTx, nil)
	mockRepo.On("WithTx", mockTx).Return(mockRepo)
	mockRepo.On("LockOrphanedChunk", mock.Anything, "chunk-999").Return(nil)
	mockStore.On("DeleteChunk", mock.Anything, "deadbeefhash").Return(nil)
	mockRepo.On("DeleteChunkRecord", mock.Anything, "chunk-999").Return(nil)

	err := svc.RunGarbageCollection(context.Background(), minAge)
	assert.NoError(t, err)
	mockRepo.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockTx.AssertExpectations(t)
}

func TestStorageService_RunGarbageCollection_SkipsChunkReReferencedSinceScan(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	// Listed as orphaned by the scan, but an upload linked it before we got the lock.
	orphan := Chunk{ID: "chunk-999", Hash: "deadbeefhash", Size: 4096}
	minAge := 10 * time.Minute

	mockTx := new(mockTx)
	mockTx.On("Rollback", mock.Anything).Return(nil)

	mockRepo.On("GetOrphanedChunks", mock.Anything, minAge).Return([]Chunk{orphan}, nil)
	mockRepo.On("BeginTx", mock.Anything).Return(mockTx, nil)
	mockRepo.On("WithTx", mockTx).Return(mockRepo)
	mockRepo.On("LockOrphanedChunk", mock.Anything, "chunk-999").Return(ErrChunkInUse)

	err := svc.RunGarbageCollection(context.Background(), minAge)
	assert.NoError(t, err)

	// The whole point: a chunk that regained a reference keeps its object and its row.
	mockStore.AssertNotCalled(t, "DeleteChunk", mock.Anything, mock.Anything)
	mockRepo.AssertNotCalled(t, "DeleteChunkRecord", mock.Anything, mock.Anything)
	mockTx.AssertNotCalled(t, "Commit", mock.Anything)
}

func TestStorageService_RunGarbageCollection_KeepsRecordWhenObjectDeleteFails(t *testing.T) {
	mockRepo := new(MockRepository)
	mockStore := new(MockObjectStore)
	svc := NewService(mockRepo, mockStore, nil)

	orphan := Chunk{ID: "chunk-999", Hash: "deadbeefhash", Size: 4096}
	minAge := 10 * time.Minute

	mockTx := new(mockTx)
	mockTx.On("Rollback", mock.Anything).Return(nil)

	mockRepo.On("GetOrphanedChunks", mock.Anything, minAge).Return([]Chunk{orphan}, nil)
	mockRepo.On("BeginTx", mock.Anything).Return(mockTx, nil)
	mockRepo.On("WithTx", mockTx).Return(mockRepo)
	mockRepo.On("LockOrphanedChunk", mock.Anything, "chunk-999").Return(nil)
	mockStore.On("DeleteChunk", mock.Anything, "deadbeefhash").Return(errors.New("minio unavailable"))

	err := svc.RunGarbageCollection(context.Background(), minAge)
	// One bad chunk must not abort the sweep.
	assert.NoError(t, err)

	// The row must survive so the chunk is retried next sweep rather than being
	// silently dropped while its object is still present.
	mockRepo.AssertNotCalled(t, "DeleteChunkRecord", mock.Anything, mock.Anything)
	mockTx.AssertNotCalled(t, "Commit", mock.Anything)
}
