package circuitbreaker

import (
	"testing"
	"time"
)

// TestCircuitBreaker_TripsOnConsecutiveFailures verifies that once enough
// requests have failed to breach the configured failure threshold rate,
// the breaker transitions from StateClosed to StateOpen.
func TestCircuitBreaker_TripsOnConsecutiveFailures(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 200*time.Millisecond)

	if cb.CurrentState() != StateClosed {
		t.Fatalf("expected initial state to be StateClosed, got %s", cb.CurrentState())
	}

	// minRequestsThreshold is internally set to 5; record 5 consecutive
	// failures to guarantee the failure rate (100%) breaches the 50%
	// threshold and trips the breaker.
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}

	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected state to be StateOpen after consecutive failures, got %s", cb.CurrentState())
	}

	if cb.CanExecute() {
		t.Fatal("expected CanExecute to return false while breaker is open and cooldown has not elapsed")
	}
}

// TestCircuitBreaker_DoesNotTripBelowMinimumRequests verifies that the
// breaker will not trip purely based on failure rate if too few requests
// have been observed to draw a statistically meaningful conclusion.
func TestCircuitBreaker_DoesNotTripBelowMinimumRequests(t *testing.T) {
	cb := NewCircuitBreaker(0.5, 200*time.Millisecond)

	// Only 2 failures recorded; below the internal minimum request
	// threshold of 5, so the breaker must remain closed regardless of
	// the (currently 100%) failure rate.
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.CurrentState() != StateClosed {
		t.Fatalf("expected state to remain StateClosed below minimum request threshold, got %s", cb.CurrentState())
	}

	if !cb.CanExecute() {
		t.Fatal("expected CanExecute to return true while breaker remains closed")
	}
}

// TestCircuitBreaker_TransitionsToHalfOpenAfterCooldown verifies that once
// the breaker is open, CanExecute blocks requests until the cooldown
// window elapses, after which a single canary request is permitted and
// the breaker transitions into StateHalfOpen.
func TestCircuitBreaker_TransitionsToHalfOpenAfterCooldown(t *testing.T) {
	cooldown := 100 * time.Millisecond
	cb := NewCircuitBreaker(0.5, cooldown)

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected state to be StateOpen, got %s", cb.CurrentState())
	}

	if cb.CanExecute() {
		t.Fatal("expected CanExecute to return false immediately after tripping, before cooldown elapses")
	}

	time.Sleep(cooldown + 50*time.Millisecond)

	if !cb.CanExecute() {
		t.Fatal("expected CanExecute to return true (canary request) once cooldown window has elapsed")
	}

	if cb.CurrentState() != StateHalfOpen {
		t.Fatalf("expected state to be StateHalfOpen after cooldown elapses, got %s", cb.CurrentState())
	}
}

// TestCircuitBreaker_HalfOpenSuccessClosesCircuit verifies that a
// successful canary request in StateHalfOpen resets the breaker back to
// StateClosed with cleared failure metrics.
func TestCircuitBreaker_HalfOpenSuccessClosesCircuit(t *testing.T) {
	cooldown := 100 * time.Millisecond
	cb := NewCircuitBreaker(0.5, cooldown)

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	time.Sleep(cooldown + 50*time.Millisecond)

	if !cb.CanExecute() {
		t.Fatal("expected canary request to be permitted after cooldown")
	}
	if cb.CurrentState() != StateHalfOpen {
		t.Fatalf("expected state to be StateHalfOpen, got %s", cb.CurrentState())
	}

	cb.RecordSuccess()

	if cb.CurrentState() != StateClosed {
		t.Fatalf("expected state to be StateClosed after successful canary request, got %s", cb.CurrentState())
	}

	if !cb.CanExecute() {
		t.Fatal("expected CanExecute to return true immediately after closing")
	}
}

// TestCircuitBreaker_HalfOpenFailureReopensCircuit verifies that a failed
// canary request while in StateHalfOpen immediately re-trips the breaker
// back to StateOpen.
func TestCircuitBreaker_HalfOpenFailureReopensCircuit(t *testing.T) {
	cooldown := 100 * time.Millisecond
	cb := NewCircuitBreaker(0.5, cooldown)

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	time.Sleep(cooldown + 50*time.Millisecond)

	if !cb.CanExecute() {
		t.Fatal("expected canary request to be permitted after cooldown")
	}
	if cb.CurrentState() != StateHalfOpen {
		t.Fatalf("expected state to be StateHalfOpen, got %s", cb.CurrentState())
	}

	cb.RecordFailure()

	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected state to be StateOpen after failed canary request, got %s", cb.CurrentState())
	}

	if cb.CanExecute() {
		t.Fatal("expected CanExecute to return false immediately after re-tripping")
	}
}