package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- Login limiter sweep ---

func TestLoginLimiterSweep_EvictsStaleKeepsLive(t *testing.T) {
	l := &loginLimiter{entries: map[string]*limitEntry{}}
	now := time.Now()

	l.entries["stale@example.com"] = &limitEntry{failures: 3, lastFail: now.Add(-loginLimitWindow - time.Minute)}
	l.entries["ancient@example.com"] = &limitEntry{failures: 1, lastFail: now.Add(-24 * time.Hour)}
	l.entries["live@example.com"] = &limitEntry{failures: 2, lastFail: now.Add(-time.Minute)}
	l.entries["edge@example.com"] = &limitEntry{failures: 1, lastFail: now.Add(-loginLimitWindow + time.Second)}

	if evicted := l.sweep(now); evicted != 2 {
		t.Errorf("evicted = %d, want 2", evicted)
	}

	if _, ok := l.entries["stale@example.com"]; ok {
		t.Error("a closed window must be evicted")
	}
	if _, ok := l.entries["ancient@example.com"]; ok {
		t.Error("a long-closed window must be evicted")
	}
	live, ok := l.entries["live@example.com"]
	if !ok {
		t.Fatal("an open window must be kept")
	}
	if live.failures != 2 {
		t.Errorf("failures = %d, want the count preserved", live.failures)
	}
	if _, ok := l.entries["edge@example.com"]; !ok {
		t.Error("an entry just inside the window must be kept")
	}
}

func TestLoginLimiterSweep_EmptyMap(t *testing.T) {
	l := &loginLimiter{}
	if evicted := l.sweep(time.Now()); evicted != 0 {
		t.Errorf("evicted = %d, want 0", evicted)
	}
}

// TestLoginLimiterSweep_PreservesLockout pins that sweeping does not hand a
// locked-out caller a fresh set of attempts.
func TestLoginLimiterSweep_PreservesLockout(t *testing.T) {
	l := &loginLimiter{entries: map[string]*limitEntry{}}
	for i := 0; i < maxLoginAttempts; i++ {
		l.recordFailure("victim@example.com")
	}
	if l.check("victim@example.com") {
		t.Fatal("expected the caller to be locked out before the sweep")
	}

	l.sweep(time.Now())

	if l.check("victim@example.com") {
		t.Error("sweeping must not clear a lockout whose window is still open")
	}
}

// TestLoginLimiter_BoundedByCap covers growth between sweeps: a caller inventing
// a new address every attempt must not be able to grow the map without limit.
func TestLoginLimiter_BoundedByCap(t *testing.T) {
	l := &loginLimiter{entries: map[string]*limitEntry{}}

	for i := 0; i < maxLimiterEntries+500; i++ {
		l.recordFailure(fmt.Sprintf("user-%d@example.com", i))
	}

	if len(l.entries) > maxLimiterEntries {
		t.Errorf("entries = %d, want no more than %d", len(l.entries), maxLimiterEntries)
	}
}

// TestLoginLimiter_CapEvictsStaleFirst pins that the cap prefers dropping
// entries whose window has already closed over live ones.
func TestLoginLimiter_CapEvictsStaleFirst(t *testing.T) {
	l := &loginLimiter{entries: map[string]*limitEntry{}}
	stale := time.Now().Add(-loginLimitWindow - time.Minute)

	for i := 0; i < maxLimiterEntries-1; i++ {
		l.entries[fmt.Sprintf("stale-%d@example.com", i)] = &limitEntry{failures: 1, lastFail: stale}
	}
	l.entries["live@example.com"] = &limitEntry{failures: 4, lastFail: time.Now()}

	l.recordFailure("newcomer@example.com")

	if _, ok := l.entries["live@example.com"]; !ok {
		t.Error("a live entry must survive while stale ones are available to drop")
	}
	if _, ok := l.entries["newcomer@example.com"]; !ok {
		t.Error("the new failure must be recorded")
	}
}

// --- Janitor loop ---

// fakeTokenCleaner stands in for the database-backed token store.
type fakeTokenCleaner struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeTokenCleaner) Cleanup(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeTokenCleaner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestJanitor_CleanOnceRunsBothJobs(t *testing.T) {
	tokens := &fakeTokenCleaner{}
	l := &loginLimiter{entries: map[string]*limitEntry{
		"stale@example.com": {failures: 2, lastFail: time.Now().Add(-loginLimitWindow - time.Minute)},
		"live@example.com":  {failures: 1, lastFail: time.Now()},
	}}
	j := &Janitor{tokens: tokens, limiter: l, interval: time.Hour}

	j.CleanOnce(context.Background())

	if tokens.callCount() != 1 {
		t.Errorf("Cleanup calls = %d, want 1", tokens.callCount())
	}
	if _, ok := l.entries["stale@example.com"]; ok {
		t.Error("expected the stale entry to be swept")
	}
	if _, ok := l.entries["live@example.com"]; !ok {
		t.Error("expected the live entry to be kept")
	}
}

// TestJanitor_TokenFailureDoesNotSkipSweep pins that the two jobs are
// independent: a database outage must not leave the limiter growing.
func TestJanitor_TokenFailureDoesNotSkipSweep(t *testing.T) {
	tokens := &fakeTokenCleaner{err: errors.New("database is down")}
	l := &loginLimiter{entries: map[string]*limitEntry{
		"stale@example.com": {failures: 2, lastFail: time.Now().Add(-loginLimitWindow - time.Minute)},
	}}
	j := &Janitor{tokens: tokens, limiter: l, interval: time.Hour}

	j.CleanOnce(context.Background())

	if len(l.entries) != 0 {
		t.Error("the limiter must still be swept when token cleanup fails")
	}
}

// TestJanitor_NoTokenStore pins that a janitor without a token store sweeps
// rather than panicking.
func TestJanitor_NoTokenStore(t *testing.T) {
	l := &loginLimiter{entries: map[string]*limitEntry{
		"stale@example.com": {failures: 2, lastFail: time.Now().Add(-loginLimitWindow - time.Minute)},
	}}
	j := &Janitor{limiter: l, interval: time.Hour}

	j.CleanOnce(context.Background())

	if len(l.entries) != 0 {
		t.Error("expected the stale entry to be swept")
	}
}

// TestNormalizeTokenCleaner_NilStore pins the typed-nil guard: a nil
// *TokenStore must become a nil interface, or the janitor's nil check passes
// and Cleanup panics on the nil pool.
func TestNormalizeTokenCleaner_NilStore(t *testing.T) {
	if got := normalizeTokenCleaner(nil); got != nil {
		t.Errorf("normalizeTokenCleaner(nil) = %v, want a nil interface", got)
	}

	h := &Handler{} // tokenStore is nil
	j := h.NewJanitor(time.Hour)
	if j.tokens != nil {
		t.Error("a handler without a token store must produce a janitor with none")
	}
	// Must not panic.
	j.CleanOnce(context.Background())
}

// TestJanitor_RunCallsCleanupOnTick drives the real loop and waits for the
// database job specifically, not just the in-memory one.
func TestJanitor_RunCallsCleanupOnTick(t *testing.T) {
	tokens := &fakeTokenCleaner{}
	j := &Janitor{tokens: tokens, limiter: &loginLimiter{}, interval: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.Run(ctx)

	deadline := time.After(2 * time.Second)
	for tokens.callCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("Cleanup was not called within 2s")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestJanitor_RunSweepsOnTick drives the real loop on a short interval and
// waits for the effect rather than sleeping a fixed amount.
func TestJanitor_RunSweepsOnTick(t *testing.T) {
	l := &loginLimiter{entries: map[string]*limitEntry{
		"stale@example.com": {failures: 2, lastFail: time.Now().Add(-loginLimitWindow - time.Minute)},
	}}
	j := &Janitor{limiter: l, interval: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		l.mu.Lock()
		swept := len(l.entries) == 0
		l.mu.Unlock()
		if swept {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the janitor did not sweep the limiter within 2s")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestJanitor_StopsOnContextCancel pins the shutdown contract: cancelling the
// context ends the goroutine, so Wait returns rather than hanging.
func TestJanitor_StopsOnContextCancel(t *testing.T) {
	j := &Janitor{limiter: &loginLimiter{}, interval: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	go j.Run(ctx)

	// Give the loop a moment to enter its select, then stop it.
	time.Sleep(5 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		j.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the janitor goroutine did not exit after its context was cancelled")
	}
}

// TestJanitor_WaitReturnsWhenNeverStarted pins that Wait on an unstarted
// janitor does not block, so a failed startup cannot hang shutdown.
func TestJanitor_WaitReturnsWhenNeverStarted(t *testing.T) {
	j := &Janitor{limiter: &loginLimiter{}, interval: time.Hour}

	done := make(chan struct{})
	go func() {
		j.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait blocked on a janitor that was never started")
	}
}

// TestHandlerNewJanitor_UsesHandlerState pins that the janitor built from a
// handler sweeps that handler's own limiter, not a copy of it.
func TestHandlerNewJanitor_UsesHandlerState(t *testing.T) {
	h := &Handler{}
	h.limiter.recordFailure("stale@example.com")
	h.limiter.entries["stale@example.com"].lastFail = time.Now().Add(-loginLimitWindow - time.Minute)

	j := h.NewJanitor(time.Hour)
	j.CleanOnce(context.Background())

	h.limiter.mu.Lock()
	defer h.limiter.mu.Unlock()
	if len(h.limiter.entries) != 0 {
		t.Errorf("entries = %d, want the handler's own limiter to have been swept", len(h.limiter.entries))
	}
}
