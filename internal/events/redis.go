package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"nimbus/internal/tracing"
)

const (
	StreamName       = "nimbus:events"
	DeadLetterStream = "nimbus:dead-letters"
	ConsumerGroup    = "nimbus-workers"
)

type Publisher interface {
	PublishFileUploaded(ctx context.Context, fileID string) error
}

type Consumer interface {
	Consume(ctx context.Context, handler func(ctx context.Context, fileID string) error) error
}

type redisBroker struct {
	client *redis.Client
}

func NewRedisBroker(addr, password string) (*redisBroker, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	// Ensure the consumer group exists
	err := client.XGroupCreateMkStream(context.Background(), StreamName, ConsumerGroup, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return nil, fmt.Errorf("failed to create consumer group: %w", err)
	}

	return &redisBroker{client: client}, nil
}

func (r *redisBroker) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *redisBroker) PublishFileUploaded(ctx context.Context, fileID string) error {
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamName,
		Values: map[string]interface{}{
			"event_type": "file_uploaded",
			"file_id":    fileID,
			"trace_id":   tracing.TraceIDFromContext(ctx),
		},
	}).Err()
}

func generateConsumerName() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "worker"
	}
	return fmt.Sprintf("%s-pid-%d", hostname, os.Getpid())
}

func (r *redisBroker) Consume(ctx context.Context, handler func(ctx context.Context, fileID string) error) error {
	consumerName := generateConsumerName()
	slog.Info("starting consumer loop", "consumer_id", consumerName, "group", ConsumerGroup, "stream", StreamName)

	// On startup, check and claim any stale messages in the PEL (> 5 minutes old)
	r.claimAndProcessStaleMessages(ctx, consumerName, handler)

	// Also run periodic check for stale messages every 3 minutes in background
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.claimAndProcessStaleMessages(ctx, consumerName, handler)
		default:
			// Read new messages from the stream, block for 2 seconds
			streams, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    ConsumerGroup,
				Consumer: consumerName,
				Streams:  []string{StreamName, ">"},
				Count:    1,
				Block:    2 * time.Second,
			}).Result()

			if errors.Is(err, redis.Nil) {
				// No messages received within block duration, continue loop
				continue
			} else if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("redis consume error: %w", err)
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					r.handleMessageWithRetry(ctx, msg, handler)
				}
			}
		}
	}
}

func (r *redisBroker) handleMessageWithRetry(ctx context.Context, msg redis.XMessage, handler func(ctx context.Context, fileID string) error) {
	eventType, _ := msg.Values["event_type"].(string)
	if eventType != "file_uploaded" {
		// Acknowledge unknown events to skip them
		r.client.XAck(ctx, StreamName, ConsumerGroup, msg.ID)
		return
	}

	fileID, _ := msg.Values["file_id"].(string)
	if fileID == "" {
		r.client.XAck(ctx, StreamName, ConsumerGroup, msg.ID)
		return
	}

	// Propagate distributed trace ID from event payload into context
	if traceID, ok := msg.Values["trace_id"].(string); ok && traceID != "" {
		ctx = tracing.WithTraceID(ctx, traceID)
	}

	const maxRetries = 3
	var err error

	for i := 0; i < maxRetries; i++ {
		if ctx.Err() != nil {
			return // Context cancelled, leave unacknowledged for clean exit
		}
		err = handler(ctx, fileID)
		if err == nil {
			// Success! ACK message and return
			r.client.XAck(ctx, StreamName, ConsumerGroup, msg.ID)
			return
		}

		if i < maxRetries-1 {
			backoff := time.Duration(1<<i) * 500 * time.Millisecond
			tracing.Logger(ctx).Warn("error processing message, retrying...", "message_id", msg.ID, "try", i+1, "max_retries", maxRetries, "error", err, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
		}
	}

	// After max retries, log failure, send to dead-letter queue, and ACK from primary stream
	tracing.Logger(ctx).Error("message failed after max retries, moving to DLQ", "message_id", msg.ID, "max_retries", maxRetries, "error", err, "dlq_stream", DeadLetterStream)

	_ = r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: DeadLetterStream,
		Values: map[string]interface{}{
			"original_id": msg.ID,
			"event_type":  eventType,
			"file_id":     fileID,
			"trace_id":    tracing.TraceIDFromContext(ctx),
			"error":       err.Error(),
			"failed_at":   time.Now().Format(time.RFC3339),
		},
	}).Err()

	r.client.XAck(ctx, StreamName, ConsumerGroup, msg.ID)
}

func (r *redisBroker) claimAndProcessStaleMessages(ctx context.Context, consumerName string, handler func(ctx context.Context, fileID string) error) {
	start := "0-0"
	for {
		if ctx.Err() != nil {
			return
		}
		messages, nextStart, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   StreamName,
			Group:    ConsumerGroup,
			Consumer: consumerName,
			MinIdle:  5 * time.Minute,
			Start:    start,
			Count:    10,
		}).Result()
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Error("error claiming stale messages from PEL", "error", err)
			}
			return
		}

		for _, msg := range messages {
			slog.Info("claimed stale message from PEL (pending > 5m)", "message_id", msg.ID)
			r.handleMessageWithRetry(ctx, msg, handler)
		}

		if nextStart == "0-0" || len(messages) == 0 {
			break
		}
		start = nextStart
	}
}
