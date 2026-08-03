-- +goose Up
-- +goose StatementBegin
CREATE TABLE chunks (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    hash VARCHAR(64) UNIQUE NOT NULL, -- SHA-256 hash
    size BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE file_chunks (
    file_id UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    chunk_id UUID NOT NULL REFERENCES chunks(id) ON DELETE RESTRICT,
    chunk_index INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (file_id, chunk_index)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE file_chunks;
DROP TABLE chunks;
-- +goose StatementEnd
