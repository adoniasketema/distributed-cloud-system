package auth

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

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
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": user.ID,
		"email": user.Email,
		"exp": time.Now().Add(time.Hour * 24).Unix(), // Expires in 24 hours
		"iat": time.Now().Unix(),
	})

	tokenString, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}
