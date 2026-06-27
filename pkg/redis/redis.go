package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/platform/gateway/config"
	"github.com/redis/go-redis/v9"
)

// Client wraps the underlying redis.Client to provide gateway-specific
// connection management and health check semantics.
type Client struct {
	raw *redis.Client
}

// pingTimeout bounds how long startup and health-check pings may block.
const pingTimeout = 5 * time.Second

// NewRedisClient parses the configured Redis URL, establishes a connection
// pool, and validates connectivity via an explicit Ping before returning.
func NewRedisClient(cfg *config.Config) (*Client, error) {
	opts, err := redis.ParseURL(cfg.Database.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis url: %w", err)
	}

	raw := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	if err := raw.Ping(ctx).Err(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &Client{raw: raw}, nil
}

// Raw exposes the underlying redis.Client for components that require
// direct access to the full driver API (pipelines, pub/sub, scripting).
func (c *Client) Raw() *redis.Client {
	return c.raw
}

// HealthCheck performs a lightweight liveness verification against the
// Redis instance, suitable for invocation from periodic health endpoints.
func (c *Client) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := c.raw.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis health check failed: %w", err)
	}
	return nil
}

// Close releases all resources held by the underlying connection pool.
// It should be invoked once during graceful application shutdown.
func (c *Client) Close() error {
	if c.raw == nil {
		return nil
	}
	return c.raw.Close()
}