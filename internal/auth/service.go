package auth

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// TokenTTL is how long an issued access token stays valid.
const TokenTTL = 24 * time.Hour

// SigningMethod is the algorithm tokens are issued with. Validators must accept this and
// nothing else, so that a token cannot be presented under a weaker algorithm.
var SigningMethod = jwt.SigningMethodHS256

// Claims is the payload of a Nimbus access token.
//
// It is a typed struct rather than jwt.MapClaims so the issuer and the validator agree on
// the shape by construction. With MapClaims every field arrives as interface{} after the
// JSON round trip and each reader has to type-assert: a claim whose Go type does not survive
// that round trip (any number becomes float64) silently fails its assertion, and the failure
// looks like a missing claim rather than a bug.
type Claims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// UserID returns the subject, which is the authenticated user's ID.
func (c *Claims) UserID() string {
	return c.Subject
}

// Service defines the interface for our Auth Service
type Service interface {
	Register(ctx context.Context, email, password string) (*User, error)
	Login(ctx context.Context, email, password string) (string, error)
}

type UserRepository interface {
	CreateUser(ctx context.Context, email, passwordHash string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
}

type authService struct {
	repo      UserRepository
	jwtSecret string
}

// NewService creates a new auth service
func NewService(repo UserRepository, jwtSecret string) Service {
	return &authService{
		repo:      repo,
		jwtSecret: jwtSecret,
	}
}

// Register creates a new user, hashing their password securely
func (s *authService) Register(ctx context.Context, email, password string) (*User, error) {
	// 1. Hash the password
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	passwordHash := string(hashBytes)

	// 2. Save user to database
	user, err := s.repo.CreateUser(ctx, email, passwordHash)
	if err != nil {
		return nil, err
	}

	return user, nil
}

// Login verifies credentials and returns a JWT token if successful
func (s *authService) Login(ctx context.Context, email, password string) (string, error) {
	// 1. Fetch user by email
	user, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		return "", errors.New("invalid credentials")
	}

	// 2. Compare passwords
	err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	if err != nil {
		return "", errors.New("invalid credentials")
	}

	// 3. Generate JWT
	now := time.Now()
	claims := Claims{
		Email: user.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
		},
	}

	token := jwt.NewWithClaims(SigningMethod, claims)

	tokenString, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}
