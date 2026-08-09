package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// MockUserRepository is a mock implementation of UserRepository
type MockUserRepository struct {
	mock.Mock
}

func (m *MockUserRepository) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	args := m.Called(ctx, email, passwordHash)
	if user := args.Get(0); user != nil {
		return user.(*User), args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockUserRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	args := m.Called(ctx, email)
	if user := args.Get(0); user != nil {
		return user.(*User), args.Error(1)
	}
	return nil, args.Error(1)
}

func TestAuthService_Register(t *testing.T) {
	mockRepo := new(MockUserRepository)
	svc := NewService(mockRepo, "test-secret")

	email := "test@example.com"
	password := "password123"

	// We expect CreateUser to be called. We use mock.Anything for the passwordHash
	// because it's randomly salted bcrypt.
	mockRepo.On("CreateUser", mock.Anything, email, mock.Anything).Return(&User{
		ID:    "123",
		Email: email,
	}, nil)

	user, err := svc.Register(context.Background(), email, password)

	assert.NoError(t, err)
	assert.NotNil(t, user)
	assert.Equal(t, "123", user.ID)
	assert.Equal(t, email, user.Email)
	mockRepo.AssertExpectations(t)
}

func TestAuthService_Register_PropagatesEmailTaken(t *testing.T) {
	mockRepo := new(MockUserRepository)
	svc := NewService(mockRepo, "test-secret")

	email := "taken@example.com"
	mockRepo.On("CreateUser", mock.Anything, email, mock.Anything).Return((*User)(nil), ErrEmailTaken)

	user, err := svc.Register(context.Background(), email, "password123")

	// The handler distinguishes this from a server fault to answer 409, so the sentinel
	// must survive the service layer unwrapped.
	assert.ErrorIs(t, err, ErrEmailTaken)
	assert.Nil(t, user)
	mockRepo.AssertExpectations(t)
}

func TestAuthService_Login(t *testing.T) {
	mockRepo := new(MockUserRepository)
	secret := "test-secret"
	svc := NewService(mockRepo, secret)

	email := "test@example.com"
	password := "password123"

	// Create a valid bcrypt hash for the test password
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)

	// Mock getting the user
	mockRepo.On("GetUserByEmail", mock.Anything, email).Return(&User{
		ID:           "123",
		Email:        email,
		PasswordHash: string(hash),
	}, nil)

	token, err := svc.Login(context.Background(), email, password)

	assert.NoError(t, err)
	assert.NotEmpty(t, token)

	// Verify the token
	claims := &Claims{}
	parsedToken, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	assert.NoError(t, err)
	assert.True(t, parsedToken.Valid)

	assert.Equal(t, "123", claims.UserID())
	assert.Equal(t, email, claims.Email)
	assert.Equal(t, SigningMethod.Alg(), parsedToken.Method.Alg())

	// The subject must survive the JSON round trip as a string; this is the assertion that
	// would have caught a numeric user ID silently arriving as float64.
	assert.IsType(t, "", claims.Subject)

	require.NotNil(t, claims.ExpiresAt)
	require.NotNil(t, claims.IssuedAt)
	assert.WithinDuration(t, claims.IssuedAt.Add(TokenTTL), claims.ExpiresAt.Time, time.Second)

	mockRepo.AssertExpectations(t)
}

func TestAuthService_Login_InvalidCredentials(t *testing.T) {
	mockRepo := new(MockUserRepository)
	svc := NewService(mockRepo, "test-secret")

	email := "test@example.com"

	// Return user not found
	mockRepo.On("GetUserByEmail", mock.Anything, email).Return((*User)(nil), errors.New("user not found"))

	token, err := svc.Login(context.Background(), email, "wrongpassword")

	assert.Error(t, err)
	assert.Equal(t, "invalid credentials", err.Error())
	assert.Empty(t, token)
}
