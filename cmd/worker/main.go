package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"nimbus/internal/ai"
	"nimbus/internal/config"
	"nimbus/internal/database"
	"nimbus/internal/events"
	"nimbus/internal/storage"
	"nimbus/internal/worker"
)

func main() {
	// Set up production JSON structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, relying on environment variables")
	}

	cfg := config.Load()

	slog.Info("starting nimbus background worker...")

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
	storageRepo := storage.NewRepository(dbPool)
	storageSvc := storage.NewService(storageRepo, minioClient, nil)

	aiRepo := ai.NewRepository(dbPool)
	aiSvc := ai.NewService(aiRepo, cfg.OpenRouterAPIKey, cfg.OpenRouterModel)

	// 3. Initialize Processor
	processor := worker.NewProcessor(storageSvc, aiSvc)

	// 4. Start Consuming Events with Graceful Shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-quit
		slog.Info("received signal, shutting down worker gracefully...", "signal", sig.String())
		cancel() // This causes the Redis Consume loop to exit via ctx.Done()
	}()

	// 5. Start Periodic Garbage Collection for Orphaned Chunks (> 24 hours old, checked every 6 hours)
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				slog.Info("running periodic garbage collection...")
				if err := storageSvc.RunGarbageCollection(ctx, 24*time.Hour); err != nil {
					slog.Error("garbage collection error", "error", err)
				}
			}
		}
	}()

	slog.Info("listening for file_uploaded events on Redis Stream...")

	err = redisBroker.Consume(ctx, processor.ProcessFileUploaded)
	if err != nil && err != context.Canceled {
		slog.Error("worker crashed", "error", err)
		os.Exit(1)
	}

	slog.Info("worker exited cleanly")
}
