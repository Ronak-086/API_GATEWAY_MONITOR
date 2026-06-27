package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// ServerConfig holds HTTP server related settings.
type ServerConfig struct {
	Port int `mapstructure:"SERVER_PORT"`
}

// DatabaseConfig holds connection strings for backing data stores.
type DatabaseConfig struct {
	RedisURL    string `mapstructure:"REDIS_URL"`
	PostgresURL string `mapstructure:"POSTGRES_URL"`
}

// RateLimiterConfig holds token-bucket rate limiting parameters.
type RateLimiterConfig struct {
	Capacity   int     `mapstructure:"RATE_LIMITER_CAPACITY"`
	RefillRate float64 `mapstructure:"RATE_LIMITER_REFILL_RATE"`
}

// CircuitBreakerConfig holds circuit breaker tuning parameters.
type CircuitBreakerConfig struct {
	FailureThresholdRate float64       `mapstructure:"CIRCUIT_BREAKER_FAILURE_THRESHOLD_RATE"`
	CooldownTimeout      time.Duration `mapstructure:"CIRCUIT_BREAKER_COOLDOWN_TIMEOUT"`
}

// Config is the root configuration object aggregating all gateway settings.
type Config struct {
	Server         ServerConfig         `mapstructure:",squash"`
	Database       DatabaseConfig       `mapstructure:",squash"`
	RateLimiter    RateLimiterConfig    `mapstructure:",squash"`
	CircuitBreaker CircuitBreakerConfig `mapstructure:",squash"`
}

// setDefaults registers sane defaults so the gateway can boot without a
// fully populated environment file.
func setDefaults(v *viper.Viper) {
	v.SetDefault("SERVER_PORT", 8080)

	v.SetDefault("REDIS_URL", "redis://localhost:6379/0")
	v.SetDefault("POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/gateway?sslmode=disable")

	v.SetDefault("RATE_LIMITER_CAPACITY", 100)
	v.SetDefault("RATE_LIMITER_REFILL_RATE", 10.0)

	v.SetDefault("CIRCUIT_BREAKER_FAILURE_THRESHOLD_RATE", 0.5)
	v.SetDefault("CIRCUIT_BREAKER_COOLDOWN_TIMEOUT", 30*time.Second)
}

// LoadConfig reads configuration from environment variables (optionally
// backed by a .env file) and returns a fully populated Config instance.
func LoadConfig() (*Config, error) {
	v := viper.New()

	v.SetConfigName(".env")
	v.SetConfigType("env")
	v.AddConfigPath(".")

	setDefaults(v)

	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// validate performs basic sanity checks on the loaded configuration.
func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server port %d is out of valid range", c.Server.Port)
	}
	if c.Database.RedisURL == "" {
		return fmt.Errorf("redis url must not be empty")
	}
	if c.Database.PostgresURL == "" {
		return fmt.Errorf("postgres url must not be empty")
	}
	if c.RateLimiter.Capacity <= 0 {
		return fmt.Errorf("rate limiter capacity must be greater than zero")
	}
	if c.RateLimiter.RefillRate <= 0 {
		return fmt.Errorf("rate limiter refill rate must be greater than zero")
	}
	if c.CircuitBreaker.FailureThresholdRate <= 0 || c.CircuitBreaker.FailureThresholdRate > 1 {
		return fmt.Errorf("circuit breaker failure threshold rate must be between 0 and 1")
	}
	if c.CircuitBreaker.CooldownTimeout <= 0 {
		return fmt.Errorf("circuit breaker cooldown timeout must be greater than zero")
	}
	return nil
}
