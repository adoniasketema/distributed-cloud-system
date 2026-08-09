package worker

import (
	"context"
	"fmt"
	"io"
	"mime"
	"strings"

	"nimbus/internal/ai"
	"nimbus/internal/storage"
	"nimbus/internal/tracing"
)

type Processor struct {
	storageSvc storage.Service
	aiSvc      ai.Service
}

func NewProcessor(storageSvc storage.Service, aiSvc ai.Service) *Processor {
	return &Processor{
		storageSvc: storageSvc,
		aiSvc:      aiSvc,
	}
}

func (p *Processor) ProcessFileUploaded(ctx context.Context, fileID string) error {
	logger := tracing.Logger(ctx).With("file_id", fileID, "event_type", "file_uploaded")
	logger.Info("worker began processing file uploaded event")

	// 1. Download the file from storage (internal access — no ownership check needed for system workers)
	reader, info, err := p.storageSvc.DownloadFileInternal(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}

	// 2. Only text can be summarised. Feeding a PNG or a zip to the model spends tokens on
	// mojibake and stores a meaningless summary, so skip non-text files outright. Returning
	// nil (not an error) acknowledges the event rather than retrying it three times and
	// dead-lettering something that will never succeed.
	if !isTextualContentType(info.ContentType) {
		logger.Info("skipping AI enrichment for non-text file", "content_type", info.ContentType)
		return nil
	}

	// 3. Deterministically read up to 100KB using io.ReadFull to prevent short reads on network multi-readers
	buffer := make([]byte, 100*1024)
	n, err := io.ReadFull(reader, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("failed to read file content: %w", err)
	}

	// The 100KB cut is at an arbitrary byte offset, so it can split a multi-byte rune and
	// leave an invalid tail. Drop invalid sequences rather than shipping them to the model.
	textContent := strings.ToValidUTF8(string(buffer[:n]), "")
	if strings.TrimSpace(textContent) == "" {
		logger.Info("skipping AI enrichment for empty file")
		return nil
	}

	// 4. Send to AI Service to generate and save embedding
	err = p.aiSvc.GenerateAndSaveEmbedding(ctx, fileID, textContent)
	if err != nil {
		return fmt.Errorf("ai processing failed: %w", err)
	}

	logger.Info("worker successfully generated and saved embedding")
	return nil
}

// textualContentTypes are the non-text/* media types whose payload is still plain text.
var textualContentTypes = map[string]bool{
	"application/json":       true,
	"application/xml":        true,
	"application/javascript": true,
	"application/x-sh":       true,
	"application/yaml":       true,
	"application/x-yaml":     true,
}

// isTextualContentType reports whether a media type describes content worth summarising.
// Content types are stored as produced by http.DetectContentType, so they carry parameters
// ("text/plain; charset=utf-8") that have to be stripped before comparison.
func isTextualContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// An unparseable type tells us nothing; treat it as not-text rather than guessing.
		return false
	}
	mediaType = strings.ToLower(mediaType)

	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	// Structured suffixes: application/vnd.api+json, image/svg+xml, and friends.
	if strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") {
		return true
	}
	return textualContentTypes[mediaType]
}
