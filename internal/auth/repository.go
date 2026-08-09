package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUserNotFound = errors.New("user not found")
	// ErrEmailTaken means the users.email unique constraint rejected the insert.
	ErrEmailTaken = errors.New("email already registered")
)

// User represents a user in the database
type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type repository struct {
	db *pgxpool.Pool
}

// NewRepository creates a new user repository interface implementation
func NewRepository(db *pgxpool.Pool) UserRepository {
	return &repository{db: db}
}

// CreateUser inserts a new user into the database
func (r *repository) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	query := `
		INSERT INTO users (email, password_hash)
		VALUES ($1, $2)
		RETURNING id, email, password_hash, created_at, updated_at
	`

	var user User
	err := r.db.QueryRow(ctx, query, email, passwordHash).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.CreatedAt,
		&user.UpdatedAt,
	)

	if err != nil {
		// The unique index on users.email is the authority on whether an address is free.
		// Checking first and inserting second would leave a window for two concurrent
		// registrations of the same address, so let the constraint decide and translate
		// its error. Matching SQLSTATE 23505 rather than the message text, which is
		// driver- and locale-dependent.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	return &user, nil
}

// GetUserByEmail fetches a user by their email address
func (r *repository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	query := `
		SELECT id, email, password_hash, created_at, updated_at
		FROM users
		WHERE email = $1
	`

	var user User
	err := r.db.QueryRow(ctx, query, email).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.CreatedAt,
		&user.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return &user, nil
}
