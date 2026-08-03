-- +goose Up
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN content_type VARCHAR(255) NOT NULL DEFAULT 'application/octet-stream';

CREATE INDEX idx_file_embeddings_jsonb ON file_embeddings USING gin (embedding);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_file_embeddings_jsonb;
ALTER TABLE files DROP COLUMN content_type;
-- +goose StatementEnd
