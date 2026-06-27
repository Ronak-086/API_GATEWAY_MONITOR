package limiter

import (
	"context"
	"fmt"
	"time"

	redisclient "github.com/platform/gateway/pkg/redis"
	"github.com/redis/go-redis/v9"
)

// tokenBucketScript implements an atomic token-bucket rate limiter inside
// Redis using Lua. Running the entire calculation server-side eliminates
// race conditions that would otherwise occur between read-modify-write
// steps performed by separate gateway instances.
//
// KEYS[1] - the rate limit bucket key (e.g. "ratelimit:<client_ip>")
// ARGV[1] - capacity        (maximum number of tokens the bucket can hold)
// ARGV[2] - refill_rate     (tokens replenished per second)
// ARGV[3] - now             (current unix timestamp, fractional seconds)
// ARGV[4] - requested       (tokens requested for this call, normally 1)
//
// Returns 1 if the request is allowed (and decrements the bucket), or 0
// if the bucket does not currently hold enough tokens.
const tokenBucketScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

local bucket = redis.call("HMGET", key, "tokens", "timestamp")
local tokens = tonumber(bucket[1])
local last_ts = tonumber(bucket[2])

if tokens == nil then
  tokens = capacity
  last_ts = now
end

local elapsed = now - last_ts
if elapsed < 0 then
  elapsed = 0
end

local refill = elapsed * refill_rate
tokens = math.min(capacity, tokens + refill)

local allowed = 0
if tokens >= requested then
  tokens = tokens - requested
  allowed = 1
end

redis.call("HMSET", key, "tokens", tostring(tokens), "timestamp", tostring(now))

-- Bucket is allowed to go idle for the time it takes to refill from empty
-- to full; after that the state is irrelevant and can safely expire.
local ttl = 0
if refill_rate > 0 then
  ttl = math.ceil(capacity / refill_rate) + 1
else
  ttl = 60
end
redis.call("EXPIRE", key, ttl)

return allowed
`

// RateLimiter enforces distributed token-bucket rate limiting backed by
// Redis, allowing consistent limits across multiple gateway instances.
type RateLimiter struct {
	redisClient *redisclient.Client
	scriptSHA   string
}

// evalTimeout bounds how long a single rate-limit evaluation may block
// waiting on Redis before the call is treated as failed.
const evalTimeout = 2 * time.Second

// NewRateLimiter constructs a RateLimiter bound to the provided Redis
// client wrapper. The Lua script is loaded (cached) lazily on first use
// via EVALSHA with automatic fallback to EVAL.
func NewRateLimiter(redisClient *redisclient.Client) *RateLimiter {
	return &RateLimiter{
		redisClient: redisClient,
	}
}

// Allow evaluates whether a single request identified by key (e.g. client
// IP, API key, or tenant ID) should be permitted under a token-bucket
// policy with the given capacity (max burst size) and refillRate (tokens
// replenished per second). It returns true if the request is allowed.
func (rl *RateLimiter) Allow(ctx context.Context, key string, capacity int, refillRate float64) (bool, error) {
	if capacity <= 0 {
		return false, fmt.Errorf("capacity must be greater than zero")
	}
	if refillRate <= 0 {
		return false, fmt.Errorf("refill rate must be greater than zero")
	}
	if key == "" {
		return false, fmt.Errorf("rate limit key must not be empty")
	}

	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()

	bucketKey := fmt.Sprintf("ratelimit:%s", key)
	now := float64(time.Now().UnixNano()) / float64(time.Second)

	rdb := rl.redisClient.Raw()

	result, err := rdb.Eval(ctx, tokenBucketScript, []string{bucketKey},
		capacity,
		refillRate,
		now,
		1,
	).Result()

	if err != nil {
		if err == redis.Nil {
			return false, fmt.Errorf("rate limiter received nil response from redis")
		}
		return false, fmt.Errorf("rate limiter script execution failed: %w", err)
	}

	allowed, ok := result.(int64)
	if !ok {
		return false, fmt.Errorf("unexpected rate limiter script return type: %T", result)
	}

	return allowed == 1, nil
}