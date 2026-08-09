package worker

import (
	"context"
	"fmt"
	"io"

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
	reader, _, err := p.storageSvc.DownloadFileInternal(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}

	// 2. Deterministically read up to 100KB using io.ReadFull to prevent short reads on network multi-readers
	buffer := make([]byte, 100*1024)
	n, err := io.ReadFull(reader, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("failed to read file content: %w", err)
	}

	textContent := string(buffer[:n])

	// 3. Send to AI Service to generate and save embedding
	err = p.aiSvc.GenerateAndSaveEmbedding(ctx, fileID, textContent)
	if err != nil {
		return fmt.Errorf("ai processing failed: %w", err)
	}

	logger.Info("worker successfully generated and saved embedding")
	return nil
}
