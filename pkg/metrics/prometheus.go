package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RequestsTotal tracks the total number of HTTP requests processed by the
// gateway, partitioned by method, routing path, and resulting status code.
var RequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_http_requests_total",
		Help: "Total number of HTTP requests processed by the gateway, labeled by method, path, and status code.",
	},
	[]string{"method", "path", "status_code"},
)

// RequestDuration tracks request processing latency in seconds, partitioned
// by method and routing path. Bucket boundaries are tuned to give accurate
// P95/P99 resolution across typical gateway response times (sub-millisecond
// to several seconds).
var RequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "gateway_http_request_duration_seconds",
		Help: "Histogram of HTTP request processing durations in seconds, labeled by method and path.",
		Buckets: []float64{
			0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
			1, 2.5, 5, 10,
		},
	},
	[]string{"method", "path"},
)

// metricsResponseWriter wraps http.ResponseWriter to capture the status
// code ultimately written by downstream handlers, so it can be recorded
// against the request-counter label set after the handler completes.
type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func newMetricsResponseWriter(w http.ResponseWriter) *metricsResponseWriter {
	return &metricsResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

func (mw *metricsResponseWriter) WriteHeader(code int) {
	if !mw.wroteHeader {
		mw.statusCode = code
		mw.wroteHeader = true
	}
	mw.ResponseWriter.WriteHeader(code)
}

func (mw *metricsResponseWriter) Write(b []byte) (int, error) {
	if !mw.wroteHeader {
		mw.statusCode = http.StatusOK
		mw.wroteHeader = true
	}
	return mw.ResponseWriter.Write(b)
}

// routeLabel normalizes the path used as a metric label. In production
// deployments with highly dynamic path segments (IDs, slugs), this should
// be replaced with the matched route pattern rather than the raw path to
// avoid unbounded label cardinality. For the current static-route gateway
// surface, the raw path is safe to use directly.
func routeLabel(r *http.Request) string {
	if r.URL.Path == "" {
		return "/"
	}
	return r.URL.Path
}

// MetricsMiddleware wraps an http.Handler, automatically recording request
// counts and processing latency for every request that passes through it.
func MetricsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			mw := newMetricsResponseWriter(w)
			next.ServeHTTP(mw, r)

			duration := time.Since(start).Seconds()
			path := routeLabel(r)

			RequestDuration.WithLabelValues(r.Method, path).Observe(duration)
			RequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(mw.statusCode)).Inc()
		})
	}
}