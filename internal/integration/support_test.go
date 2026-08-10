//go:build integration

// Package integration exercises the repository layer against a real PostgreSQL instance.
//
// These tests exist because the unit suite mocks the repositories, which means it validates
// how the services call the database but never whether the SQL itself is valid. A query the
// server refuses to plan - a missing cast, a renamed column, a constraint that does not fire -
// is indistinguishable from a correct one when the repository is a mock. One such bug (42P18,
// an untyped parameter inside a polymorphic function) reached the end-to-end suite before
// anything caught it.
//
// Run with: make test-integration
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// testDatabaseURL is where the suite looks for a migrated database.
func testDatabaseURL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://nimbus:password@localhost:5432/nimbus_db?sslmode=disable"
}

// newPool connects to the test database, skipping the suite when none is reachable so a
// developer without Docker running gets a skip rather than a wall of failures.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, testDatabaseURL())
	if err != nil {
		t.Skipf("no test database available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database available at %s: %v", testDatabaseURL(), err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// createUser inserts a user with a unique address and removes it when the test ends.
// Every other fixture hangs off a user, and the ON DELETE CASCADE chain means deleting it
// cleans up folders, files, and chunk links without the test tracking them individually.
func createUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	email := fmt.Sprintf("it-%s@example.test", uuid.NewString())
	var userID string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&userID)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

// insertFile creates a file row directly so tests can control status and timestamps, which
// the service layer deliberately does not expose.
func insertFile(t *testing.T, pool *pgxpool.Pool, userID string, folderID *string, name, status string) string {
	t.Helper()

	var fileID string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO files (user_id, folder_id, name, content_type, size, status)
		 VALUES ($1, $2, $3, 'text/plain', 0, $4) RETURNING id`,
		userID, folderID, name, status).Scan(&fileID)
	require.NoError(t, err)
	return fileID
}

// insertChunk creates a chunk row with a caller-chosen age, so garbage collection tests can
// produce a chunk that is already old enough to collect without waiting.
func insertChunk(t *testing.T, pool *pgxpool.Pool, hash string, age time.Duration) string {
	t.Helper()

	var chunkID string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO chunks (hash, size, created_at) VALUES ($1, 1, $2) RETURNING id`,
		hash, time.Now().Add(-age)).Scan(&chunkID)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM chunks WHERE id = $1`, chunkID)
	})
	return chunkID
}

// setEmbedding attaches enrichment output to a file so search can match against it.
func setEmbedding(t *testing.T, pool *pgxpool.Pool, fileID, embeddingJSON string) {
	t.Helper()

	_, err := pool.Exec(context.Background(),
		`INSERT INTO file_embeddings (file_id, embedding) VALUES ($1, $2::jsonb)
		 ON CONFLICT (file_id) DO UPDATE SET embedding = EXCLUDED.embedding`,
		fileID, embeddingJSON)
	require.NoError(t, err)
}

// uniqueHash returns a chunk hash that cannot collide with another test's.
func uniqueHash() string {
	return fmt.Sprintf("%064x", uuid.New().ID())
}
