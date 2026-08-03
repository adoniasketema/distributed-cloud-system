package ai

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository interface {
	SaveEmbedding(ctx context.Context, fileID string, embeddingJSON []byte) error
}

type repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) Repository {
	return &repository{db: db}
}

func (r *repository) SaveEmbedding(ctx context.Context, fileID string, embeddingJSON []byte) error {
	query := `
		INSERT INTO file_embeddings (file_id, embedding)
		VALUES ($1, $2)
		ON CONFLICT (file_id) DO UPDATE SET embedding = EXCLUDED.embedding
	`
	_, err := r.db.Exec(ctx, query, fileID, embeddingJSON)
	if err != nil {
		return fmt.Errorf("failed to save AI metadata to db: %w", err)
	}
	return nil
}
