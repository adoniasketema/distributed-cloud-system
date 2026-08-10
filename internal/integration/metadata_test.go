//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nimbus/internal/metadata"
	"nimbus/internal/storage"
)

func TestSearchFiles_MatchesExactTag(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	fileID := insertFile(t, pool, userID, nil, "quarterly.txt", storage.StatusReady)
	setEmbedding(t, pool, fileID, `{"summary":"A quarterly report","tags":["finance","quarterly"]}`)

	// This is the regression that shipped: the JSONB containment branch used an untyped
	// parameter inside json_build_array, and PostgreSQL rejected the entire statement with
	// 42P18 - so every search, not just tag searches, returned 500.
	files, err := repo.SearchFiles(context.Background(), userID, "finance")
	require.NoError(t, err)

	require.Len(t, files, 1)
	assert.Equal(t, fileID, files[0].ID)
}

func TestSearchFiles_MatchesNameAndSummary(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	byName := insertFile(t, pool, userID, nil, "budget-plan.txt", storage.StatusReady)
	bySummary := insertFile(t, pool, userID, nil, "notes.txt", storage.StatusReady)
	setEmbedding(t, pool, bySummary, `{"summary":"Thoughts on the budget","tags":["misc"]}`)

	byNameResults, err := repo.SearchFiles(context.Background(), userID, "budget-plan")
	require.NoError(t, err)
	require.Len(t, byNameResults, 1)
	assert.Equal(t, byName, byNameResults[0].ID)

	// "budget" appears in one file's name and the other's summary, so both should match.
	both, err := repo.SearchFiles(context.Background(), userID, "budget")
	require.NoError(t, err)
	assert.Len(t, both, 2)
}

func TestSearchFiles_WildcardIsTreatedAsLiteral(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	insertFile(t, pool, userID, nil, "alpha.txt", storage.StatusReady)
	insertFile(t, pool, userID, nil, "beta.txt", storage.StatusReady)
	literal := insertFile(t, pool, userID, nil, "100%-done.txt", storage.StatusReady)

	// Unescaped, "%" is an ILIKE wildcard and would return every file the user owns.
	files, err := repo.SearchFiles(context.Background(), userID, "%")
	require.NoError(t, err)

	require.Len(t, files, 1, "a percent sign must match literally, not as a wildcard")
	assert.Equal(t, literal, files[0].ID)
}

func TestSearchFiles_UnderscoreIsTreatedAsLiteral(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	insertFile(t, pool, userID, nil, "abc.txt", storage.StatusReady)
	literal := insertFile(t, pool, userID, nil, "a_c.txt", storage.StatusReady)

	// "_" matches any single character in ILIKE, so unescaped this would also match abc.txt.
	files, err := repo.SearchFiles(context.Background(), userID, "a_c")
	require.NoError(t, err)

	require.Len(t, files, 1)
	assert.Equal(t, literal, files[0].ID)
}

func TestSearchFiles_ExcludesOtherUsersAndIncompleteUploads(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)
	otherID := createUser(t, pool)

	ready := insertFile(t, pool, userID, nil, "shared-name.txt", storage.StatusReady)
	insertFile(t, pool, userID, nil, "shared-name-partial.txt", storage.StatusUploading)
	insertFile(t, pool, otherID, nil, "shared-name-theirs.txt", storage.StatusReady)

	files, err := repo.SearchFiles(context.Background(), userID, "shared-name")
	require.NoError(t, err)

	require.Len(t, files, 1, "must exclude other users' files and unfinished uploads")
	assert.Equal(t, ready, files[0].ID)
}

func TestListFiles_ExcludesIncompleteUploads(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	ready := insertFile(t, pool, userID, nil, "done.txt", storage.StatusReady)
	insertFile(t, pool, userID, nil, "half.txt", storage.StatusUploading)

	files, err := repo.ListFiles(context.Background(), userID, nil)
	require.NoError(t, err)

	require.Len(t, files, 1, "a partially uploaded file must not appear in a listing")
	assert.Equal(t, ready, files[0].ID)
}

func TestCreateFolder_DuplicateNameIsReportedAsSuch(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	_, err := repo.CreateFolder(context.Background(), userID, nil, "Documents")
	require.NoError(t, err)

	// Relies on the real UNIQUE constraint firing and on 23505 being classified correctly;
	// a mock cannot verify either.
	_, err = repo.CreateFolder(context.Background(), userID, nil, "Documents")
	assert.ErrorIs(t, err, metadata.ErrDuplicateName)
}

func TestCreateFolder_DuplicateNameRejectedInsideSubfolderToo(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	parent, err := repo.CreateFolder(context.Background(), userID, nil, "Parent")
	require.NoError(t, err)

	_, err = repo.CreateFolder(context.Background(), userID, &parent.ID, "Child")
	require.NoError(t, err)

	_, err = repo.CreateFolder(context.Background(), userID, &parent.ID, "Child")
	assert.ErrorIs(t, err, metadata.ErrDuplicateName)
}

func TestCreateFolder_SameNameAllowedInDifferentParents(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userID := createUser(t, pool)

	a, err := repo.CreateFolder(context.Background(), userID, nil, "A")
	require.NoError(t, err)
	b, err := repo.CreateFolder(context.Background(), userID, nil, "B")
	require.NoError(t, err)

	// Uniqueness is per directory, so the same name under two different parents is fine.
	// The root-level fix must not over-correct into global uniqueness.
	_, err = repo.CreateFolder(context.Background(), userID, &a.ID, "Shared")
	require.NoError(t, err)
	_, err = repo.CreateFolder(context.Background(), userID, &b.ID, "Shared")
	assert.NoError(t, err)
}

func TestCreateFolder_SameNameAllowedForDifferentUsers(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	userA := createUser(t, pool)
	userB := createUser(t, pool)

	_, err := repo.CreateFolder(context.Background(), userA, nil, "Documents")
	require.NoError(t, err)

	// The constraint is (user_id, parent_id, name); it must not collide across users.
	_, err = repo.CreateFolder(context.Background(), userB, nil, "Documents")
	assert.NoError(t, err)
}

func TestVerifyFolderOwnership(t *testing.T) {
	pool := newPool(t)
	repo := metadata.NewRepository(pool)
	owner := createUser(t, pool)
	attacker := createUser(t, pool)

	folder, err := repo.CreateFolder(context.Background(), owner, nil, "Private")
	require.NoError(t, err)

	t.Run("owner is accepted", func(t *testing.T) {
		assert.NoError(t, repo.VerifyFolderOwnership(context.Background(), owner, folder.ID))
	})

	t.Run("another user is rejected", func(t *testing.T) {
		assert.ErrorIs(t, repo.VerifyFolderOwnership(context.Background(), attacker, folder.ID), metadata.ErrNotFound)
	})

	t.Run("malformed id is rejected as not found", func(t *testing.T) {
		// A non-UUID string against a UUID column raises 22P02. Surfacing that as a 500
		// would tell a caller their id was well-formed but not theirs, which is a
		// distinction they should not be able to draw.
		err := repo.VerifyFolderOwnership(context.Background(), owner, "not-a-uuid")
		assert.ErrorIs(t, err, metadata.ErrNotFound)
	})
}
