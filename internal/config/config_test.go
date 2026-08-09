package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_JWTSecretHasNoDefault(t *testing.T) {
	t.Setenv("JWT_SECRET", "")

	cfg := Load()

	// The whole point: an unset secret must stay empty so validation can reject it, rather
	// than silently resolving to a value published in this repository.
	assert.Empty(t, cfg.JWTSecret)
	assert.Error(t, cfg.ValidateForAPI())
}

func TestValidateForAPI_RejectsUnsafeSecrets(t *testing.T) {
	cases := []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"the placeholder from .env.example", "change-me-in-production-use-a-long-random-string"},
		{"placeholder with different casing", "Change-Me-In-Production-Use-A-Long-Random-String"},
		{"placeholder with surrounding space", "  change-me-in-production-use-a-long-random-string  "},
		{"common weak value", "secret"},
		{"too short", "abcdefghijklmnopqrstuvwxyz12345"}, // 31 chars
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{JWTSecret: tc.secret}
			err := cfg.ValidateForAPI()

			require.Error(t, err)
			// The message has to tell the operator how to fix it, not just that it is wrong.
			assert.Contains(t, err.Error(), "openssl rand")
		})
	}
}

func TestValidateForAPI_AcceptsStrongSecret(t *testing.T) {
	cfg := &Config{JWTSecret: strings.Repeat("a", MinJWTSecretLength)}

	assert.NoError(t, cfg.ValidateForAPI())
}

func TestGetEnvList(t *testing.T) {
	t.Run("unset yields nil", func(t *testing.T) {
		t.Setenv("TEST_LIST", "")
		assert.Nil(t, getEnvList("TEST_LIST"))
	})

	t.Run("splits and trims", func(t *testing.T) {
		t.Setenv("TEST_LIST", " 10.0.0.0/8 , 172.16.0.0/12 ")
		assert.Equal(t, []string{"10.0.0.0/8", "172.16.0.0/12"}, getEnvList("TEST_LIST"))
	})

	t.Run("drops empty entries", func(t *testing.T) {
		t.Setenv("TEST_LIST", "10.0.0.0/8,,")
		assert.Equal(t, []string{"10.0.0.0/8"}, getEnvList("TEST_LIST"))
	})
}
