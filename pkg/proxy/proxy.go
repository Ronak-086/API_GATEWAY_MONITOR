package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/platform/gateway/pkg/logger"
	"go.uber.org/zap"
)

// ReverseProxyManager owns a configured httputil.ReverseProxy bound to a
// single upstream target, along with routing metadata used to rewrite
// incoming requests before they are dispatched downstream.
type ReverseProxyManager struct {
	target      *url.URL
	proxy       *httputil.ReverseProxy
	stripPrefix string
}

// proxyTransportTimeout bounds how long the proxy will wait for the
// downstream target to establish a connection and respond.
const (
	dialTimeout           = 5 * time.Second
	responseHeaderTimeout = 10 * time.Second
)

// NewProxyManager constructs a ReverseProxyManager targeting the given
// upstream URL. The optional stripPrefix, if non-empty, is removed from
// the incoming request path before forwarding.
func NewProxyManager(targetURL string) (*ReverseProxyManager, error) {
	return NewProxyManagerWithPrefix(targetURL, "")
}

// NewProxyManagerWithPrefix behaves like NewProxyManager but additionally
// strips the given routing prefix (e.g. "/api/v1") from the request path
// prior to forwarding it to the upstream target.
func NewProxyManagerWithPrefix(targetURL string, stripPrefix string) (*ReverseProxyManager, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy target url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("proxy target url must include scheme and host: %q", targetURL)
	}

	rpm := &ReverseProxyManager{
		target:      parsed,
		stripPrefix: stripPrefix,
	}

	rp := &httputil.ReverseProxy{
		Director:     rpm.director,
		ErrorHandler: rpm.errorHandler,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: responseHeaderTimeout,
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   50,
			IdleConnTimeout:       90 * time.Second,
		},
	}

	rpm.proxy = rp
	return rpm, nil
}

// director rewrites the outbound request so it targets the configured
// upstream while preserving and augmenting client-identifying headers.
func (rpm *ReverseProxyManager) director(req *http.Request) {
	originalHost := req.Host
	clientIP := clientIPFromRequest(req)

	req.URL.Scheme = rpm.target.Scheme
	req.URL.Host = rpm.target.Host
	req.Host = rpm.target.Host

	if rpm.stripPrefix != "" && strings.HasPrefix(req.URL.Path, rpm.stripPrefix) {
		trimmed := strings.TrimPrefix(req.URL.Path, rpm.stripPrefix)
		if trimmed == "" {
			trimmed = "/"
		}
		req.URL.Path = trimmed
	}

	if existing := req.Header.Get("X-Forwarded-For"); existing != "" && clientIP != "" {
		req.Header.Set("X-Forwarded-For", existing+", "+clientIP)
	} else if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}

	if req.URL.Scheme != "" {
		req.Header.Set("X-Forwarded-Proto", req.URL.Scheme)
	}
	if originalHost != "" {
		req.Header.Set("X-Forwarded-Host", originalHost)
	}

	req.Header.Set("X-Gateway-Forwarded", "true")

	req.Header.Del("Connection")
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authenticate")
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Te")
	req.Header.Del("Trailer")
	req.Header.Del("Transfer-Encoding")
	req.Header.Del("Upgrade")
}

// errorHandler intercepts downstream transport failures (connection
// refusals, dropped connections, timeouts) and maps them onto an
// appropriate gateway-level HTTP status, while emitting structured logs.
func (rpm *ReverseProxyManager) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	log := logger.Log
	if log == nil {
		log = zap.NewNop()
	}

	fields := []zap.Field{
		zap.String("upstream", rpm.target.String()),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.String("client_ip", clientIPFromRequest(r)),
		zap.Error(err),
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded), isTimeoutError(err):
		log.Error("upstream request timed out", fields...)
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte("gateway timeout: upstream did not respond in time"))

	case isConnectionRefusedOrReset(err):
		log.Error("upstream connection failed", fields...)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway: failed to reach upstream service"))

	default:
		log.Error("unhandled proxy error", fields...)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway: unexpected proxy error"))
	}
}

// ServeHTTP allows ReverseProxyManager to be used directly as an
// http.Handler, delegating to the internally configured ReverseProxy.
func (rpm *ReverseProxyManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rpm.proxy.ServeHTTP(w, r)
}

// Target returns the upstream URL this manager forwards requests to.
func (rpm *ReverseProxyManager) Target() *url.URL {
	return rpm.target
}

// clientIPFromRequest extracts the originating client IP from the request's
// remote address, stripping the port component if present.
func clientIPFromRequest(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// isTimeoutError reports whether err represents a network-level timeout.
func isTimeoutError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// isConnectionRefusedOrReset reports whether err represents a failure to
// establish or maintain a TCP connection to the upstream target.
func isConnectionRefusedOrReset(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "broken pipe")
}