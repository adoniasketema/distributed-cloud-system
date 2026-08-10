.PHONY: build test test-integration e2e check-infra dev-up dev-down migrate migrate-down clean

# Connection string used by the migration targets. Override to point at another database:
#   make migrate DATABASE_URL=postgres://...
DATABASE_URL ?= postgres://nimbus:password@localhost:5432/nimbus_db?sslmode=disable

# Default target executes unit test verification followed by binary compilation
all: test build

# Compile HTTP API, Redis stream worker, and infrastructure CLI binaries into ./bin/
build:
	@echo "[BUILD] Compiling application binaries..."
	@mkdir -p bin
	go build -o bin/api_server ./cmd/api
	go build -o bin/worker_server ./cmd/worker
	go build -o bin/infra_check ./cmd/infra
	@echo "[BUILD] Binaries generated successfully in ./bin/"

# Run unit test suite across all modules in deterministic single-threaded test execution
test:
	@echo "[TEST] Executing unit test suite..."
	go test -count=1 ./...

# Apply database migrations. Uses goose via `go run` so it does not become a build
# dependency of the application itself.
migrate:
	@echo "[MIGRATE] Applying database migrations..."
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir migrations postgres "$(DATABASE_URL)" up

# Roll back the most recent migration
migrate-down:
	@echo "[MIGRATE] Rolling back most recent migration..."
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir migrations postgres "$(DATABASE_URL)" down

# Run repository tests against a real PostgreSQL instance. Requires `make dev-up` and
# `make migrate` first; the suite skips itself if no database is reachable.
test-integration:
	@echo "[TEST] Executing integration suite against live PostgreSQL..."
	go test -count=1 -tags=integration ./internal/integration/...

# Run live end-to-end integration test suite against running Docker infrastructure
e2e:
	@echo "[TEST] Launching end-to-end integration test suite..."
	bash scripts/e2e.sh

# Verify live connectivity to PostgreSQL, MinIO Object Store, and Redis Broker
check-infra:
	@echo "[INFRA] Verifying infrastructure service health..."
	go run ./cmd/infra/main.go

# Spin up background development infrastructure (PostgreSQL, MinIO, Redis)
dev-up:
	@echo "[INFRA] Starting local containers in detached mode..."
	docker-compose up -d
	@echo "[INFRA] Containers operational. S3 Console available on http://localhost:9001"

# Shut down and clean up local development containers
dev-down:
	@echo "[INFRA] Terminating local infrastructure containers..."
	docker-compose down

# Clean temporary build outputs and compiled binaries
clean:
	@echo "[CLEANUP] Removing build outputs and temporary artifacts..."
	@rm -rf bin/ api_server worker_server dummy_upload.txt downloaded.txt
	@echo "[CLEANUP] Workspace restored to pristine state."
