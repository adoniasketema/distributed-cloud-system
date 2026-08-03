package config

import "os"

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
		JWTSecret:        getEnv("JWT_SECRET", "change-me-in-production-use-a-long-random-string"),
		APIPort:          getEnv("API_PORT", "8080"),
		OpenRouterAPIKey: getEnv("OPEN_ROUTER_API_KEY", ""),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
