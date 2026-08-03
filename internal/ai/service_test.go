package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type MockRepository struct {
	mock.Mock
}

func (m *MockRepository) SaveEmbedding(ctx context.Context, fileID string, embeddingJSON []byte) error {
	args := m.Called(ctx, fileID, embeddingJSON)
	return args.Error(0)
}

func TestGenerateAndSaveEmbedding_Success(t *testing.T) {
	expectedJSON := `{"summary":"Test summary","tags":["test","cloud"]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key-123", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
		respObj := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": expectedJSON}},
			},
		}
		json.NewEncoder(w).Encode(respObj)
	}))
	defer ts.Close()

	mockRepo := new(MockRepository)
	mockRepo.On("SaveEmbedding", mock.Anything, "file-100", []byte(expectedJSON)).Return(nil)

	svc := &service{
		repo:   mockRepo,
		apiKey: "test-key-123",
		apiURL: ts.URL,
	}

	err := svc.GenerateAndSaveEmbedding(context.Background(), "file-100", "Sample content")
	assert.NoError(t, err)
	mockRepo.AssertExpectations(t)
}

func TestGenerateAndSaveEmbedding_NoAPIKey(t *testing.T) {
	mockRepo := new(MockRepository)
	svc := NewService(mockRepo, "")

	err := svc.GenerateAndSaveEmbedding(context.Background(), "file-100", "Sample content")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OpenRouter API key is not configured")
}
