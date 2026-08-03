package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
	parsedToken, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	assert.NoError(t, err)
	assert.True(t, parsedToken.Valid)
	
	claims := parsedToken.Claims.(jwt.MapClaims)
	assert.Equal(t, "123", claims["sub"])
	assert.Equal(t, email, claims["email"])
	
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
