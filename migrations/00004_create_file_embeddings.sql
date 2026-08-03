-- +goose Up
-- +goose StatementBegin
CREATE TABLE file_embeddings (
    file_id UUID PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    embedding JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE file_embeddings;
-- +goose StatementEnd
