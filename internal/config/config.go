package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds all application configuration values.
// Values are loaded from environment variables with sensible defaults for local development.
type Config struct {
	// Database
	DatabaseURL string

	// MinIO / Object Storage
	MinIOEndpoint  string
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOUseSSL    bool

	// Redis
	RedisAddr     string
	RedisPassword string

	// Auth
	JWTSecret string

	// Server
	APIPort string

	// TrustedProxyCIDRs lists the reverse proxies whose X-Forwarded-For header may be
	// believed when identifying a client for rate limiting. Empty means trust none and
	// always use the direct peer address, which is correct for a directly exposed server.
	TrustedProxyCIDRs []string

	// AI
	OpenRouterAPIKey string
}

// Load reads configuration from environment variables, falling back to local dev defaults.
func Load() *Config {
	return &Config{
		DatabaseURL:      getEnv("DATABASE_URL", "postgres://nimbus:password@localhost:5432/nimbus_db?sslmode=disable"),
		MinIOEndpoint:    getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccessKey:   getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinIOSecretKey:   getEnv("MINIO_SECRET_KEY", "minioadmin"),
		MinIOUseSSL:      getEnv("MINIO_USE_SSL", "false") == "true",
		RedisAddr:        getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:    getEnv("REDIS_PASSWORD", ""),
		// Deliberately no fallback. A default signing key that ships in the repository is
		// a published secret: anyone who reads it can forge a token for any user, and the
		// service would start silently rather than telling you.
		JWTSecret:        os.Getenv("JWT_SECRET"),
		APIPort:          getEnv("API_PORT", "8080"),
		OpenRouterAPIKey: getEnv("OPEN_ROUTER_API_KEY", ""),

		TrustedProxyCIDRs: getEnvList("TRUSTED_PROXY_CIDRS"),
	}
}

// MinJWTSecretLength is the shortest signing key accepted. HS256 keys should carry at least
// as much entropy as the hash they feed, so anything under 256 bits is rejected.
const MinJWTSecretLength = 32

// knownInsecureJWTSecrets are placeholder values that have appeared in this repository's
// documentation. They are public, so they must never be accepted as a real key however long
// they are - copying the example verbatim is the most likely way to end up insecure.
var knownInsecureJWTSecrets = map[string]bool{
	"change-me-in-production-use-a-long-random-string": true,
	"changeme": true,
	"secret":   true,
}

// ValidateForAPI checks the settings the API server cannot safely start without.
//
// It is separate from Load, and separate from the worker's requirements, because the worker
// and the infra checker do not sign or verify tokens and should not be made to supply a key
// they never use.
func (c *Config) ValidateForAPI() error {
	switch {
	case c.JWTSecret == "":
		return fmt.Errorf("JWT_SECRET is required: generate one with `openssl rand -base64 48`")
	case knownInsecureJWTSecrets[strings.ToLower(strings.TrimSpace(c.JWTSecret))]:
		return fmt.Errorf("JWT_SECRET is set to a placeholder from this repository's docs and is therefore public: generate one with `openssl rand -base64 48`")
	case len(c.JWTSecret) < MinJWTSecretLength:
		return fmt.Errorf("JWT_SECRET must be at least %d characters, got %d: generate one with `openssl rand -base64 48`", MinJWTSecretLength, len(c.JWTSecret))
	}
	return nil
}

// getEnvList reads a comma-separated environment variable into a slice, dropping empty
// entries. An unset or blank variable yields nil.
func getEnvList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
