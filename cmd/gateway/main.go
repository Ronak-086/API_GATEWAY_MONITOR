package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/platform/gateway/config"
	"github.com/platform/gateway/internal/middleware"
	"github.com/platform/gateway/pkg/circuitbreaker"
	"github.com/platform/gateway/pkg/limiter"
	"github.com/platform/gateway/pkg/logger"
	"github.com/platform/gateway/pkg/metrics"
	"github.com/platform/gateway/pkg/proxy"
	redisclient "github.com/platform/gateway/pkg/redis"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

const shutdownTimeout = 10 * time.Second

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		panic("failed to load configuration: " + err.Error())
	}

	env := os.Getenv("APP_ENV")
	if err := logger.InitLogger(env); err != nil {
		panic("failed to initialize logger: " + err.Error())
	}
	defer logger.Sync()

	log := logger.Log
	log.Info("configuration loaded",
		zap.Int("server_port", cfg.Server.Port),
		zap.String("env", env),
	)

	redisCli, err := redisclient.NewRedisClient(cfg)
	if err != nil {
		log.Fatal("failed to initialize redis client", zap.Error(err))
	}
	log.Info("redis connection established", zap.String("redis_url", cfg.Database.RedisURL))

	rateLimiter := limiter.NewRateLimiter(redisCli)

	circuitBreaker := circuitbreaker.NewCircuitBreaker(
		cfg.CircuitBreaker.FailureThresholdRate,
		cfg.CircuitBreaker.CooldownTimeout,
	)

	upstreamURL := os.Getenv("GATEWAY_UPSTREAM_URL")
	if upstreamURL == "" {
		upstreamURL = "http://localhost:9000"
	}

	proxyManager, err := proxy.NewProxyManager(upstreamURL)
	if err != nil {
		log.Fatal("failed to initialize reverse proxy manager", zap.Error(err))
	}
	log.Info("reverse proxy configured", zap.String("upstream", proxyManager.Target().String()))

	mux := http.NewServeMux()
	registerRoutes(mux, log, redisCli)

	// /metrics is intentionally mounted outside the rate-limit / circuit-
	// breaker / application metrics chain so that scraping engines always
	// have unobstructed, low-overhead access to gateway telemetry.
	mux.Handle("/metrics", promhttp.Handler())

	protectedHandler := middleware.RateLimitMiddleware(rateLimiter, cfg)(
		middleware.CircuitBreakerMiddleware(circuitBreaker)(
			proxyManager,
		),
	)
	instrumentedHandler := metrics.MetricsMiddleware()(protectedHandler)
	mux.Handle("/", instrumentedHandler)

	srv := &http.Server{
		Addr:         formatAddr(cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		log.Info("starting http server", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-quit:
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
	case err := <-serverErrCh:
		closeRedis(log, redisCli)
		if err != nil {
			log.Error("server encountered a fatal error", zap.Error(err))
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	log.Info("attempting graceful shutdown", zap.Duration("timeout", shutdownTimeout))

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("graceful shutdown failed, forcing close", zap.Error(err))
		if closeErr := srv.Close(); closeErr != nil {
			log.Error("forced close also failed", zap.Error(closeErr))
		}
		closeRedis(log, redisCli)
		os.Exit(1)
	}

	closeRedis(log, redisCli)
	log.Info("server shutdown completed cleanly")
}

// closeRedis releases the redis connection pool and logs the outcome.
// It is safe to call exactly once per shutdown path.
func closeRedis(log *zap.Logger, cli *redisclient.Client) {
	if cli == nil {
		return
	}
	if err := cli.Close(); err != nil {
		log.Error("error closing redis connection", zap.Error(err))
		return
	}
	log.Info("redis connection closed cleanly")
}

// registerRoutes wires baseline health/readiness endpoints into the
// provided multiplexer. These endpoints intentionally bypass the
// rate-limiting and circuit-breaker middleware stack so that orchestration
// platforms can always probe gateway liveness/readiness.
func registerRoutes(mux *http.ServeMux, log *zap.Logger, redisCli *redisclient.Client) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if err := redisCli.HealthCheck(ctx); err != nil {
			log.Warn("readiness check failed", zap.Error(err))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
}

// formatAddr converts a port integer into a listen address string.
func formatAddr(port int) string {
	return ":" + itoa(port)
}

// itoa avoids importing strconv solely for this single conversion path's
// readability preference; kept minimal and explicit.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}