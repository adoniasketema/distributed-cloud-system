-- +goose Up
-- +goose StatementBegin
-- Search matches file names with a leading-wildcard ILIKE, which no btree index can serve,
-- so every search was a sequential scan over the files table. A trigram GIN index makes
-- that pattern indexable.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX idx_files_name_trgm ON files USING gin (name gin_trgm_ops);

-- Search and directory listings always filter by user_id, which had no supporting index.
CREATE INDEX idx_files_user_id ON files (user_id);
CREATE INDEX idx_folders_user_id ON folders (user_id);

-- Garbage collection looks up file_chunks by chunk_id (the orphan scan's LEFT JOIN and the
-- per-chunk reference re-check). The primary key is (file_id, chunk_index), so chunk_id had
-- no index of its own and both queries scanned the whole table.
CREATE INDEX idx_file_chunks_chunk_id ON file_chunks (chunk_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_file_chunks_chunk_id;
DROP INDEX IF EXISTS idx_folders_user_id;
DROP INDEX IF EXISTS idx_files_user_id;
DROP INDEX IF EXISTS idx_files_name_trgm;
-- +goose StatementEnd
