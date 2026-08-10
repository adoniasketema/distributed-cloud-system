-- +goose Up
-- +goose StatementBegin
-- The UNIQUE(user_id, parent_id, name) constraints were intended to prevent duplicate names
-- within a directory, but SQL treats NULL as distinct from NULL, so they never fired for the
-- root directory: (user, NULL, 'Documents') never conflicts with itself. Uniqueness held
-- inside subfolders and silently did not hold at the root, which is where most items live.
--
-- Replaced with paired partial indexes: one covering the non-root case, one covering the
-- root case with parent_id omitted from the key entirely so there is no NULL to compare.
-- PostgreSQL 15 could express this as UNIQUE NULLS NOT DISTINCT, but partial indexes work
-- on every supported version.
--
-- If this migration fails with a uniqueness violation, the database already contains
-- duplicates that the broken constraint allowed. Find them with:
--   SELECT user_id, name, count(*) FROM folders WHERE parent_id IS NULL
--   GROUP BY 1,2 HAVING count(*) > 1;
-- and the equivalent on files with folder_id IS NULL, then rename or remove them first.

ALTER TABLE folders DROP CONSTRAINT folders_user_id_parent_id_name_key;

CREATE UNIQUE INDEX folders_unique_name_in_parent
    ON folders (user_id, parent_id, name)
    WHERE parent_id IS NOT NULL;

CREATE UNIQUE INDEX folders_unique_name_at_root
    ON folders (user_id, name)
    WHERE parent_id IS NULL;

ALTER TABLE files DROP CONSTRAINT files_user_id_folder_id_name_key;

CREATE UNIQUE INDEX files_unique_name_in_folder
    ON files (user_id, folder_id, name)
    WHERE folder_id IS NOT NULL;

CREATE UNIQUE INDEX files_unique_name_at_root
    ON files (user_id, name)
    WHERE folder_id IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS files_unique_name_at_root;
DROP INDEX IF EXISTS files_unique_name_in_folder;
ALTER TABLE files ADD CONSTRAINT files_user_id_folder_id_name_key UNIQUE (user_id, folder_id, name);

DROP INDEX IF EXISTS folders_unique_name_at_root;
DROP INDEX IF EXISTS folders_unique_name_in_parent;
ALTER TABLE folders ADD CONSTRAINT folders_user_id_parent_id_name_key UNIQUE (user_id, parent_id, name);
-- +goose StatementEnd
