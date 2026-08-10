//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nimbus/internal/auth"
	"nimbus/internal/metadata"
	"nimbus/internal/storage"
)

func TestGetOrCreateChunk_DeduplicatesByHash(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)
	hash := uniqueHash()

	firstID, isNew, err := repo.GetOrCreateChunk(context.Background(), hash, 1024)
	require.NoError(t, err)
	assert.True(t, isNew, "first insert of a hash is new")
	t.Cleanup(func() { _ = repo.DeleteChunkRecord(context.Background(), firstID) })

	// The second caller must be told the chunk already exists, which is what lets the
	// upload path skip sending the bytes again.
	secondID, isNew, err := repo.GetOrCreateChunk(context.Background(), hash, 1024)
	require.NoError(t, err)
	assert.False(t, isNew, "an existing hash must not be reported as new")
	assert.Equal(t, firstID, secondID, "the same hash must resolve to the same chunk row")
}

func TestLockOrphanedChunk_RejectsReferencedChunk(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)
	userID := createUser(t, pool)

	chunkID := insertChunk(t, pool, uniqueHash(), time.Hour)
	fileID := insertFile(t, pool, userID, nil, "f.bin", storage.StatusReady)
	require.NoError(t, repo.LinkFileChunk(context.Background(), fileID, chunkID, 0))

	// A chunk that gained a reference after the orphan scan listed it must not be
	// collectable, otherwise GC deletes an object a live file still points at.
	err := repo.LockOrphanedChunk(context.Background(), chunkID)
	assert.ErrorIs(t, err, storage.ErrChunkInUse)
}

func TestLockOrphanedChunk_AcceptsUnreferencedChunk(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)

	chunkID := insertChunk(t, pool, uniqueHash(), time.Hour)

	assert.NoError(t, repo.LockOrphanedChunk(context.Background(), chunkID))
}

func TestGetOrphanedChunks_RespectsAgeAndReferences(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)
	userID := createUser(t, pool)

	old := insertChunk(t, pool, uniqueHash(), 48*time.Hour)
	recent := insertChunk(t, pool, uniqueHash(), time.Minute)

	referenced := insertChunk(t, pool, uniqueHash(), 48*time.Hour)
	fileID := insertFile(t, pool, userID, nil, "f.bin", storage.StatusReady)
	require.NoError(t, repo.LinkFileChunk(context.Background(), fileID, referenced, 0))

	chunks, err := repo.GetOrphanedChunks(context.Background(), 24*time.Hour)
	require.NoError(t, err)

	found := map[string]bool{}
	for _, c := range chunks {
		found[c.ID] = true
	}

	assert.True(t, found[old], "an old unreferenced chunk should be collectable")
	// The age floor keeps the collector away from chunks belonging to an upload that is
	// still in flight, whose file row does not exist yet.
	assert.False(t, found[recent], "a recently created chunk must be left alone")
	assert.False(t, found[referenced], "a referenced chunk must never be listed")
}

func TestDeleteStaleUploads_RemovesOnlyAbandonedRows(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)
	userID := createUser(t, pool)

	stale := insertFile(t, pool, userID, nil, "abandoned.bin", storage.StatusUploading)
	fresh := insertFile(t, pool, userID, nil, "in-flight.bin", storage.StatusUploading)
	ready := insertFile(t, pool, userID, nil, "done.bin", storage.StatusReady)

	// Age only the abandoned row. updated_at is what the sweep measures, and an upload
	// still running has not touched it either - hence the generous threshold.
	_, err := pool.Exec(context.Background(),
		`UPDATE files SET updated_at = NOW() - INTERVAL '48 hours' WHERE id = $1`, stale)
	require.NoError(t, err)

	removed, err := repo.DeleteStaleUploads(context.Background(), 24*time.Hour)
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)

	assert.False(t, fileExists(t, pool, stale), "the abandoned upload should be swept")
	assert.True(t, fileExists(t, pool, fresh), "an in-flight upload must not be swept")
	assert.True(t, fileExists(t, pool, ready), "a completed file must never be swept")
}

func TestCreateFile_RejectsDuplicateNameAtRoot(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)
	userID := createUser(t, pool)

	_, err := repo.CreateFile(context.Background(), userID, nil, "report.txt", "text/plain", 0)
	require.NoError(t, err)

	// The original UNIQUE(user_id, folder_id, name) never fired here, because folder_id is
	// NULL at the root and SQL does not consider two NULLs equal.
	_, err = repo.CreateFile(context.Background(), userID, nil, "report.txt", "text/plain", 0)
	assert.ErrorIs(t, err, storage.ErrDuplicateName)
}

func TestCreateFile_AllowsSameNameInDifferentFolders(t *testing.T) {
	pool := newPool(t)
	metaRepo := metadata.NewRepository(pool)
	repo := storage.NewRepository(pool)
	userID := createUser(t, pool)

	folder, err := metaRepo.CreateFolder(context.Background(), userID, nil, "Sub")
	require.NoError(t, err)

	_, err = repo.CreateFile(context.Background(), userID, nil, "report.txt", "text/plain", 0)
	require.NoError(t, err)

	// Same name, different directory: still allowed.
	_, err = repo.CreateFile(context.Background(), userID, &folder.ID, "report.txt", "text/plain", 0)
	assert.NoError(t, err)
}

func TestGetFileInfo_ReportsStatus(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)
	userID := createUser(t, pool)

	fileID := insertFile(t, pool, userID, nil, "doc.txt", storage.StatusUploading)

	info, err := repo.GetFileInfo(context.Background(), fileID)
	require.NoError(t, err)

	// The download path refuses anything not ready, so this column has to round-trip.
	assert.Equal(t, storage.StatusUploading, info.Status)
	assert.Equal(t, "doc.txt", info.Name)
}

func TestVerifyFileOwnership_RejectsOtherUsers(t *testing.T) {
	pool := newPool(t)
	repo := storage.NewRepository(pool)
	owner := createUser(t, pool)
	attacker := createUser(t, pool)

	fileID := insertFile(t, pool, owner, nil, "private.txt", storage.StatusReady)

	assert.NoError(t, repo.VerifyFileOwnership(context.Background(), owner, fileID))
	assert.ErrorIs(t, repo.VerifyFileOwnership(context.Background(), attacker, fileID), storage.ErrAccessDenied)
}

func TestCreateUser_DuplicateEmailIsClassified(t *testing.T) {
	pool := newPool(t)
	repo := auth.NewRepository(pool)

	email := "dup-" + uniqueHash()[:12] + "@example.test"
	user, err := repo.CreateUser(context.Background(), email, "hash")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
	})

	// Depends on the real unique index firing and on 23505 being translated, which is what
	// turns a 500 into the 409 the handler returns.
	_, err = repo.CreateUser(context.Background(), email, "hash")
	assert.ErrorIs(t, err, auth.ErrEmailTaken)
}

func fileExists(t *testing.T, pool *pgxpool.Pool, fileID string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM files WHERE id = $1)`, fileID).Scan(&exists)
	require.NoError(t, err)
	return exists
}
