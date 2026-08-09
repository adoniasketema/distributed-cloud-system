package events

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConsumeBackoff_DoublesPerAttempt(t *testing.T) {
	assert.Equal(t, 250*time.Millisecond, consumeBackoff(1))
	assert.Equal(t, 500*time.Millisecond, consumeBackoff(2))
	assert.Equal(t, time.Second, consumeBackoff(3))
	assert.Equal(t, 2*time.Second, consumeBackoff(4))
}

func TestConsumeBackoff_IsCapped(t *testing.T) {
	// Without a cap the shift would keep doubling and eventually overflow into a
	// negative duration, which would make time.After fire immediately and turn the
	// backoff into a hot loop.
	for attempt := 1; attempt <= 64; attempt++ {
		backoff := consumeBackoff(attempt)
		assert.Positive(t, backoff, "attempt %d produced a non-positive backoff", attempt)
		assert.LessOrEqual(t, backoff, maxConsumeBackoff, "attempt %d exceeded the cap", attempt)
	}
}

func TestConsumeBackoff_TotalToleratesBrokerRestart(t *testing.T) {
	// The retry budget should cover a Redis restart or failover rather than exiting on the
	// first blip.
	var total time.Duration
	for attempt := 1; attempt < maxConsumeFailures; attempt++ {
		total += consumeBackoff(attempt)
	}
	assert.Greater(t, total, 30*time.Second, "retry budget is too short to survive a restart")
}

func TestGenerateConsumerName_IsNonEmptyAndDistinguishesProcesses(t *testing.T) {
	name := generateConsumerName()

	// Redis attributes pending entries to a consumer by this name, so it must be stable
	// within a process and never empty.
	assert.NotEmpty(t, name)
	assert.Contains(t, name, "-pid-")
	assert.Equal(t, name, generateConsumerName())
}
