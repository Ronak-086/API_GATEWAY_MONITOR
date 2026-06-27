package middleware

import (
	"net"
	"net/http"

	"github.com/platform/gateway/config"
	"github.com/platform/gateway/pkg/circuitbreaker"
	"github.com/platform/gateway/pkg/limiter"
	"github.com/platform/gateway/pkg/logger"
	"go.uber.org/zap"
)

// statusRecorder wraps http.ResponseWriter to capture the status code
// written by downstream handlers, enabling middleware to observe outcome
// without altering response semantics.
type statusRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wroteHeader {
		sr.statusCode = code
		sr.wroteHeader = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.statusCode = http.StatusOK
		sr.wroteHeader = true
	}
	return sr.ResponseWriter.Write(b)
}

// extractClientIP resolves the originating client IP for an inbound
// request, preferring a trusted X-Forwarded-For entry when present and
// falling back to the raw remote address.
func extractClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// RateLimitMiddleware enforces a per-client token-bucket rate limit using
// the supplied distributed RateLimiter. Requests exceeding the configured
// capacity/refill rate are rejected with HTTP 429 before reaching any
// downstream handler.
func RateLimitMiddleware(rl *limiter.RateLimiter, cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log := logger.Log

			clientIP := extractClientIP(r)
			if clientIP == "" {
				clientIP = "unknown"
			}

			allowed, err := rl.Allow(r.Context(), clientIP, cfg.RateLimiter.Capacity, cfg.RateLimiter.RefillRate)
			if err != nil {
				if log != nil {
					log.Error("rate limiter evaluation failed",
						zap.String("client_ip", clientIP),
						zap.Error(err),
					)
				}
				// Fail open on infrastructure errors so a Redis outage does
				// not take down the entire gateway; downstream protections
				// (circuit breaker) remain in place.
				next.ServeHTTP(w, r)
				return
			}

			if !allowed {
				if log != nil {
					log.Warn("request rejected by rate limiter",
						zap.String("client_ip", clientIP),
						zap.String("path", r.URL.Path),
					)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate limit exceeded","status":429}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// CircuitBreakerMiddleware guards downstream calls with the supplied
// CircuitBreaker. If the breaker denies execution, the request is
// rejected immediately with HTTP 503. Otherwise the request proceeds and
// the resulting status code is fed back into the breaker as a success or
// failure signal.
func CircuitBreakerMiddleware(cb *circuitbreaker.CircuitBreaker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log := logger.Log

			if !cb.CanExecute() {
				if log != nil {
					log.Warn("request rejected by circuit breaker",
						zap.String("path", r.URL.Path),
						zap.String("state", cb.CurrentState().String()),
					)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"service temporarily unavailable","status":503}`))
				return
			}

			recorder := newStatusRecorder(w)
			next.ServeHTTP(recorder, r)

			if recorder.statusCode >= 500 {
				cb.RecordFailure()
				if log != nil {
					log.Warn("downstream call recorded as failure",
						zap.Int("status_code", recorder.statusCode),
						zap.String("path", r.URL.Path),
						zap.String("state", cb.CurrentState().String()),
					)
				}
				return
			}

			cb.RecordSuccess()
		})
	}
}