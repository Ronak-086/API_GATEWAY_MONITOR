package circuitbreaker

import (
	"sync"
	"time"
)

type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

type CircuitBreaker struct {
	mu sync.RWMutex

	failureThresholdRate float64
	cooldownTimeout      time.Duration
	minRequestsThreshold  int

	state              State
	consecutiveFailures int
	totalRequests       int
	totalFailures       int
	lastStateChange     time.Time
	lastFailureTime     time.Time
}

func NewCircuitBreaker(failureThresholdRate float64, cooldownTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		failureThresholdRate: failureThresholdRate,
		cooldownTimeout:      cooldownTimeout,
		minRequestsThreshold:  5,
		state:                StateClosed,
		lastStateChange:      time.Now(),
	}
}

func (cb *CircuitBreaker) CanExecute() bool {
	cb.mu.RLock()
	state := cb.state
	lastChange := cb.lastStateChange
	cb.mu.RUnlock()

	switch state {
	case StateClosed:
		return true
	case StateHalfOpen:
		return true
	case StateOpen:
		if time.Since(lastChange) >= cb.cooldownTimeout {
			cb.mu.Lock()
			if cb.state == StateOpen && time.Since(cb.lastStateChange) >= cb.cooldownTimeout {
				cb.state = StateHalfOpen
				cb.lastStateChange = time.Now()
			}
			allow := cb.state == StateHalfOpen
			cb.mu.Unlock()
			return allow
		}
		return false
	default:
		return false
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateHalfOpen:
		cb.state = StateClosed
		cb.lastStateChange = time.Now()
		cb.consecutiveFailures = 0
		cb.totalRequests = 0
		cb.totalFailures = 0
	case StateClosed:
		cb.consecutiveFailures = 0
		cb.totalRequests++
	case StateOpen:
		// Stale success arriving after the breaker already tripped; ignore.
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateHalfOpen:
		cb.trip()
		return
	case StateOpen:
		return
	case StateClosed:
		cb.consecutiveFailures++
		cb.totalRequests++
		cb.totalFailures++

		if cb.totalRequests < cb.minRequestsThreshold {
			return
		}

		failureRate := float64(cb.totalFailures) / float64(cb.totalRequests)
		if failureRate >= cb.failureThresholdRate {
			cb.trip()
		}
	}
}

func (cb *CircuitBreaker) trip() {
	cb.state = StateOpen
	cb.lastStateChange = time.Now()
	cb.consecutiveFailures = 0
	cb.totalRequests = 0
	cb.totalFailures = 0
}

func (cb *CircuitBreaker) CurrentState() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}