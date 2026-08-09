package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"nimbus/internal/auth"
	"nimbus/internal/config"
	"nimbus/internal/database"
	"nimbus/internal/events"
	"nimbus/internal/metadata"
	"nimbus/internal/middleware"
	"nimbus/internal/storage"
	"nimbus/internal/tracing"
)

func main() {
	// Set up production JSON structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, relying on environment variables")
	}

	// Load configuration from environment
	cfg := config.Load()

	// Refuse to start rather than fall back to a weak signing key: a server that boots with
	// a guessable JWT secret accepts forged tokens for every account, and does so silently.
	if err := cfg.ValidateForAPI(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// 1. Initialize Infrastructure
	dbPool, err := database.New(cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	minioClient, err := storage.NewMinioClient(cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL)
	if err != nil {
		slog.Error("unable to connect to MinIO storage", "error", err)
		os.Exit(1)
	}

	redisBroker, err := events.NewRedisBroker(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		slog.Error("unable to connect to Redis broker", "error", err)
		os.Exit(1)
	}

	// 2. Initialize Repositories & Services
	authRepo := auth.NewRepository(dbPool)
	authSvc := auth.NewService(authRepo, cfg.JWTSecret)
	authHandler := auth.NewHandler(authSvc)

	metadataRepo := metadata.NewRepository(dbPool)
	metadataSvc := metadata.NewService(metadataRepo)
	metadataHandler := metadata.NewHandler(metadataSvc)

	storageRepo := storage.NewRepository(dbPool)
	storageSvc := storage.NewService(storageRepo, minioClient, redisBroker)
	storageHandler := storage.NewHandler(storageSvc)

	// 3. Setup Routing (Native net/http)
	mux := http.NewServeMux()

	// Application context for background tasks like rate limiter memory cleanup
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// Rate limiters. Behind a load balancer the peer address is the balancer's, so the
	// limiter needs to know which peers are proxies before it can believe X-Forwarded-For.
	trustedProxies, err := middleware.NewTrustedProxies(cfg.TrustedProxyCIDRs)
	if err != nil {
		slog.Error("invalid TRUSTED_PROXY_CIDRS", "error", err)
		os.Exit(1)
	}
	if len(cfg.TrustedProxyCIDRs) == 0 {
		slog.Info("no trusted proxies configured; rate limiting on direct peer address")
	} else {
		slog.Info("trusting proxy headers from configured networks", "cidrs", cfg.TrustedProxyCIDRs)
	}

	generalRL := middleware.NewRateLimiter(appCtx, 60, time.Minute)
	generalRL.SetTrustedProxies(trustedProxies)
	strictRL := middleware.NewRateLimiter(appCtx, 15, time.Minute)
	strictRL.SetTrustedProxies(trustedProxies)

	generalLimiter := generalRL.Middleware
	strictLimiter := strictRL.Middleware

	// Caps request bodies on the JSON endpoints. The upload route is deliberately excluded:
	// it sets its own 100MB limit.
	jsonBodyLimit := middleware.MaxBodyBytes(middleware.DefaultMaxBodyBytes)

	// Auth routes (Public - strict rate limit of 15 req/min)
	mux.Handle("POST /api/v1/auth/register", strictLimiter(jsonBodyLimit(http.HandlerFunc(authHandler.Register))))
	mux.Handle("POST /api/v1/auth/login", strictLimiter(jsonBodyLimit(http.HandlerFunc(authHandler.Login))))

	// Protected routes
	authMW := middleware.AuthMiddleware(cfg.JWTSecret)

	// Metadata routes (General rate limit of 60 req/min)
	mux.Handle("POST /api/v1/folders", generalLimiter(authMW(jsonBodyLimit(http.HandlerFunc(metadataHandler.CreateFolder)))))
	mux.Handle("GET /api/v1/directory", generalLimiter(authMW(http.HandlerFunc(metadataHandler.ListDirectory))))
	mux.Handle("DELETE /api/v1/folders/{id}", generalLimiter(authMW(http.HandlerFunc(metadataHandler.DeleteFolder))))
	mux.Handle("GET /api/v1/search", generalLimiter(authMW(http.HandlerFunc(metadataHandler.SearchFiles))))

	// Storage routes
	mux.Handle("POST /api/v1/files/upload", strictLimiter(authMW(http.HandlerFunc(storageHandler.UploadFile)))) // Strict rate limit for streaming uploads
	mux.Handle("GET /api/v1/files/{id}/download", generalLimiter(authMW(http.HandlerFunc(storageHandler.DownloadFile))))
	mux.Handle("DELETE /api/v1/files/{id}", generalLimiter(authMW(http.HandlerFunc(storageHandler.DeleteFile))))

	// Health and deep readiness probes
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		status := map[string]string{
			"status":   "OK",
			"database": "OK",
			"redis":    "OK",
			"storage":  "OK",
		}
		httpStatus := http.StatusOK

		// Readiness probes are frequently reachable without authentication, so the response
		// reports only which dependency is unhealthy - never the driver's error text, which
		// can carry hostnames, ports, connection strings or credentials. The full error goes
		// to the log, where operators can see it and outsiders cannot.
		checks := []struct {
			name string
			ping func(context.Context) error
		}{
			{"database", dbPool.Ping},
			{"redis", redisBroker.Ping},
			{"storage", minioClient.Ping},
		}

		for _, check := range checks {
			if err := check.ping(ctx); err != nil {
				tracing.Logger(r.Context()).Error("readiness check failed", "error", err, "dependency", check.name)
				status["status"] = "DEGRADED"
				status[check.name] = "UNAVAILABLE"
				httpStatus = http.StatusServiceUnavailable
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		json.NewEncoder(w).Encode(status)
	})

	// 4. Start Server with Graceful Shutdown, HTTP timeouts, and Tracing Middleware
	srv := &http.Server{
		Addr:         ":" + cfg.APIPort,
		Handler:      tracing.Middleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second, // Generous write timeout for streaming file downloads
		IdleTimeout:  120 * time.Second,
	}

	// Listen for OS shutdown signals in a goroutine
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("starting nimbus API server", "port", cfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed to start", "error", err)
			os.Exit(1)
		}
	}()

	// Block until we receive a signal
	sig := <-quit
	slog.Info("received shutdown signal, stopping gracefully", "signal", sig.String())
	appCancel() // Stop rate limiter background workers

	// Give in-flight requests 10 seconds to complete
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("server exited cleanly")
}
