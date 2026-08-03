# Stage 1: Dependency Caching & Compilation Builder
FROM golang:1.22-alpine AS builder

# Install CA certificates and time-zone data for network requests and logging
RUN apk add --no-cache git bash ca-certificates tzdata

WORKDIR /src

# Download dependencies first to maximize Docker build caching efficiency
COPY go.mod go.sum ./
RUN go mod download

# Copy project source code
COPY . .

# Build statically linked binaries with stripped debugging symbols for minimal disk space
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/api_server ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/worker_server ./cmd/worker

# Stage 2: Minimal Runtime API Service Container
FROM alpine:latest AS api

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /bin/api_server /app/api_server

EXPOSE 8080
ENTRYPOINT ["/app/api_server"]

# Stage 3: Minimal Runtime Background Worker Container
FROM alpine:latest AS worker

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /bin/worker_server /app/worker_server

ENTRYPOINT ["/app/worker_server"]
