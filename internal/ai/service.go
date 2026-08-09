package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Service interface {
	GenerateAndSaveEmbedding(ctx context.Context, fileID string, content string) error
}

type service struct {
	repo   Repository
	apiKey string
	model  string
	apiURL string
}

// NewService builds the enrichment client. The model is configuration rather than a
// constant: OpenRouter's catalogue changes, model IDs are account-dependent, and a
// hardcoded ID that has been retired fails at request time on every upload with nothing
// in the code to suggest why.
func NewService(repo Repository, apiKey, model string) Service {
	return &service{
		repo:   repo,
		apiKey: apiKey,
		model:  model,
		apiURL: "https://openrouter.ai/api/v1/chat/completions",
	}
}

func (s *service) GenerateAndSaveEmbedding(ctx context.Context, fileID string, content string) error {
	if s.apiKey == "" {
		return fmt.Errorf("OpenRouter API key is not configured")
	}
	if s.model == "" {
		return fmt.Errorf("OPENROUTER_MODEL is not configured: set it to a model id available on your account (see https://openrouter.ai/models)")
	}

	// Truncate content to avoid massive token usage on large files
	if len(content) > 5000 {
		content = content[:5000]
	}

	reqBody := map[string]interface{}{
		"model": s.model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a helpful AI assistant. Your job is to analyze the provided file content and return a JSON object with exactly two keys: 'summary' (a brief 1-sentence summary of the content) and 'tags' (an array of 3-5 relevant keyword strings). Respond ONLY with valid JSON. Do not include markdown formatting like ```json.",
			},
			{
				"role":    "user",
				"content": content,
			},
		},
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.apiURL, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter api error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit to protect against massive responses
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openrouter returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var orResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(bodyBytes, &orResp); err != nil {
		return fmt.Errorf("failed to parse openrouter response: %w", err)
	}

	if len(orResp.Choices) == 0 {
		return fmt.Errorf("no choices returned from openrouter")
	}

	aiContent := orResp.Choices[0].Message.Content

	// Ensure the response is actually valid JSON before saving to DB
	var validCheck map[string]interface{}
	if err := json.Unmarshal([]byte(aiContent), &validCheck); err != nil {
		return fmt.Errorf("AI did not return valid JSON: %s", aiContent)
	}

	// Save using repository layer
	return s.repo.SaveEmbedding(ctx, fileID, []byte(aiContent))
}
