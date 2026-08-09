package worker

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"nimbus/internal/storage"
)

type mockStorageService struct {
	mock.Mock
}

func (m *mockStorageService) UploadFile(ctx context.Context, userID string, folderID *string, name string, r io.Reader) (string, error) {
	args := m.Called(ctx, userID, folderID, name, r)
	return args.String(0), args.Error(1)
}

func (m *mockStorageService) DownloadFile(ctx context.Context, userID, fileID string) (io.Reader, storage.FileInfo, error) {
	args := m.Called(ctx, userID, fileID)
	return args.Get(0).(io.Reader), args.Get(1).(storage.FileInfo), args.Error(2)
}

func (m *mockStorageService) DownloadFileInternal(ctx context.Context, fileID string) (io.Reader, storage.FileInfo, error) {
	args := m.Called(ctx, fileID)
	return args.Get(0).(io.Reader), args.Get(1).(storage.FileInfo), args.Error(2)
}

func (m *mockStorageService) DeleteFile(ctx context.Context, userID, fileID string) error {
	args := m.Called(ctx, userID, fileID)
	return args.Error(0)
}

func (m *mockStorageService) RunGarbageCollection(ctx context.Context, minAge time.Duration) error {
	args := m.Called(ctx, minAge)
	return args.Error(0)
}

type mockAIService struct {
	mock.Mock
}

func (m *mockAIService) GenerateAndSaveEmbedding(ctx context.Context, fileID, content string) error {
	args := m.Called(ctx, fileID, content)
	return args.Error(0)
}

func TestIsTextualContentType(t *testing.T) {
	cases := []struct {
		contentType string
		want        bool
	}{
		{"text/plain; charset=utf-8", true},
		{"text/html", true},
		{"text/csv", true},
		{"application/json", true},
		{"application/vnd.api+json", true},
		{"image/svg+xml", true},
		{"APPLICATION/JSON", true},
		{"application/octet-stream", false},
		{"image/png", false},
		{"application/zip", false},
		{"application/pdf", false},
		{"video/mp4", false},
		{"", false},
		{"not a media type", false},
	}

	for _, tc := range cases {
		t.Run(tc.contentType, func(t *testing.T) {
			assert.Equal(t, tc.want, isTextualContentType(tc.contentType))
		})
	}
}

func TestProcessFileUploaded_SkipsBinaryFile(t *testing.T) {
	storageSvc := new(mockStorageService)
	aiSvc := new(mockAIService)
	p := NewProcessor(storageSvc, aiSvc)

	storageSvc.On("DownloadFileInternal", mock.Anything, "file-1").
		Return(io.Reader(bytes.NewReader([]byte{0x89, 'P', 'N', 'G'})), storage.FileInfo{Name: "logo.png", ContentType: "image/png"}, nil)

	err := p.ProcessFileUploaded(context.Background(), "file-1")

	// Returning nil acknowledges the event; retrying a PNG would never start working.
	assert.NoError(t, err)
	aiSvc.AssertNotCalled(t, "GenerateAndSaveEmbedding", mock.Anything, mock.Anything, mock.Anything)
}

func TestProcessFileUploaded_ProcessesTextFile(t *testing.T) {
	storageSvc := new(mockStorageService)
	aiSvc := new(mockAIService)
	p := NewProcessor(storageSvc, aiSvc)

	storageSvc.On("DownloadFileInternal", mock.Anything, "file-1").
		Return(io.Reader(strings.NewReader("quarterly report")), storage.FileInfo{Name: "q.txt", ContentType: "text/plain; charset=utf-8"}, nil)
	aiSvc.On("GenerateAndSaveEmbedding", mock.Anything, "file-1", "quarterly report").Return(nil)

	err := p.ProcessFileUploaded(context.Background(), "file-1")

	assert.NoError(t, err)
	aiSvc.AssertExpectations(t)
}

func TestProcessFileUploaded_SkipsEmptyFile(t *testing.T) {
	storageSvc := new(mockStorageService)
	aiSvc := new(mockAIService)
	p := NewProcessor(storageSvc, aiSvc)

	storageSvc.On("DownloadFileInternal", mock.Anything, "file-1").
		Return(io.Reader(strings.NewReader("   \n\t ")), storage.FileInfo{Name: "empty.txt", ContentType: "text/plain"}, nil)

	err := p.ProcessFileUploaded(context.Background(), "file-1")

	assert.NoError(t, err)
	aiSvc.AssertNotCalled(t, "GenerateAndSaveEmbedding", mock.Anything, mock.Anything, mock.Anything)
}

func TestProcessFileUploaded_DropsInvalidUTF8FromTruncatedTail(t *testing.T) {
	storageSvc := new(mockStorageService)
	aiSvc := new(mockAIService)
	p := NewProcessor(storageSvc, aiSvc)

	// A lone 0xFF is not valid UTF-8; it stands in for a multi-byte rune sliced by the
	// 100KB read boundary.
	content := append([]byte("caf\xc3\xa9 report"), 0xFF)
	storageSvc.On("DownloadFileInternal", mock.Anything, "file-1").
		Return(io.Reader(bytes.NewReader(content)), storage.FileInfo{Name: "n.txt", ContentType: "text/plain"}, nil)

	aiSvc.On("GenerateAndSaveEmbedding", mock.Anything, "file-1", mock.MatchedBy(func(s string) bool {
		return !strings.ContainsRune(s, '�') && strings.HasPrefix(s, "café report")
	})).Return(nil)

	err := p.ProcessFileUploaded(context.Background(), "file-1")

	assert.NoError(t, err)
	aiSvc.AssertExpectations(t)
}
