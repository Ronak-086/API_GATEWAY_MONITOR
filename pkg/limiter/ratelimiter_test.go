package limiter

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/platform/gateway/config"
	redisclient "github.com/platform/gateway/pkg/redis"
)

// newTestRedisClient spins up an in-memory miniredis instance and wires it
// through the gateway's own redisclient.NewRedisClient constructor, so the
// rate limiter is exercised against the exact same client wrapper and Lua
// scripting path used in production, without requiring a real Redis
// server during test execution.
func newTestRedisClient(t *testing.T) (*redisclient.Client, func()) {
	t.Helper()

	srv, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			RedisURL: "redis://" + srv.Addr(),
		},
	}

	cli, err := redisclient.NewRedisClient(cfg)
	if err != nil {
		srv.Close()
		t.Fatalf("failed to construct redis client against miniredis: %v", err)
	}

	cleanup := func() {
		_ = cli.Close()
		srv.Close()
	}

	return cli, cleanup
}

// TestRateLimiter_AllowsWithinCapacity verifies that requests up to the
// configured bucket capacity are permitted.
func TestRateLimiter_AllowsWithinCapacity(t *testing.T) {
	cli, cleanup := newTestRedisClient(t)
	defer cleanup()

	rl := NewRateLimiter(cli)
	ctx := context.Background()

	const capacity = 3
	const refillRate = 0.001 // negligible refill within test duration

	for i := 0; i < capacity; i++ {
		allowed, err := rl.Allow(ctx, "client-a", capacity, refillRate)
		if err != nil {
			t.Fatalf("unexpected error on request %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("expected request %d to be allowed within bucket capacity", i+1)
		}
	}
}

// TestRateLimiter_DeniesWhenBucketExhausted verifies that once the bucket
// capacity has been fully consumed, subsequent requests within the same
// refill window are rejected.
func TestRateLimiter_DeniesWhenBucketExhausted(t *testing.T) {
	cli, cleanup := newTestRedisClient(t)
	defer cleanup()

	rl := NewRateLimiter(cli)
	ctx := context.Background()

	const capacity = 2
	const refillRate = 0.001 // negligible refill within test duration

	for i := 0; i < capacity; i++ {
		allowed, err := rl.Allow(ctx, "client-b", capacity, refillRate)
		if err != nil {
			t.Fatalf("unexpected error priming bucket (request %d): %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("expected priming request %d to be allowed", i+1)
		}
	}

	allowed, err := rl.Allow(ctx, "client-b", capacity, refillRate)
	if err != nil {
		t.Fatalf("unexpected error on exhausting request: %v", err)
	}
	if allowed {
		t.Fatal("expected request to be denied once bucket capacity is exhausted")
	}
}

// TestRateLimiter_RefillsOverTime verifies that tokens replenish according
// to the configured refill rate once sufficient wall-clock time has
// elapsed, allowing a previously denied client to succeed again.
func TestRateLimiter_RefillsOverTime(t *testing.T) {
	cli, cleanup := newTestRedisClient(t)
	defer cleanup()

	rl := NewRateLimiter(cli)
	ctx := context.Background()

	const capacity = 1
	const refillRate = 20.0 // tokens per second; fast enough for a short test sleep

	allowed, err := rl.Allow(ctx, "client-c", capacity, refillRate)
	if err != nil {
		t.Fatalf("unexpected error on initial request: %v", err)
	}
	if !allowed {
		t.Fatal("expected initial request to be allowed")
	}

	denied, err := rl.Allow(ctx, "client-c", capacity, refillRate)
	if err != nil {
		t.Fatalf("unexpected error on immediate follow-up request: %v", err)
	}
	if denied {
		t.Fatal("expected immediate follow-up request to be denied before refill")
	}

	// At 20 tokens/sec, waiting 100ms replenishes ~2 tokens, comfortably
	// enough to permit the bucket (capacity 1) to allow another request.
	time.Sleep(100 * time.Millisecond)

	allowedAfterRefill, err := rl.Allow(ctx, "client-c", capacity, refillRate)
	if err != nil {
		t.Fatalf("unexpected error on post-refill request: %v", err)
	}
	if !allowedAfterRefill {
		t.Fatal("expected request to be allowed after sufficient refill time elapsed")
	}
}

// TestRateLimiter_RejectsInvalidParameters verifies that the limiter
// fails fast on invalid configuration rather than silently misbehaving.
func TestRateLimiter_RejectsInvalidParameters(t *testing.T) {
	cli, cleanup := newTestRedisClient(t)
	defer cleanup()

	rl := NewRateLimiter(cli)
	ctx := context.Background()

	cases := []struct {
		name       string
		key        string
		capacity   int
		refillRate float64
	}{
		{"zero capacity", "client-d", 0, 1.0},
		{"negative capacity", "client-d", -5, 1.0},
		{"zero refill rate", "client-d", 5, 0},
		{"negative refill rate", "client-d", 5, -1.0},
		{"empty key", "", 5, 1.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rl.Allow(ctx, tc.key, tc.capacity, tc.refillRate)
			if err == nil {
				t.Fatalf("expected an error for case %q, got nil", tc.name)
			}
		})
	}
}
