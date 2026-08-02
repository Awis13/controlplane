package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Janitor removes auth state that has outlived its usefulness: expired token
// rows in the database and stale entries in the in-memory login limiter.
// Neither is cleaned up by the request paths, so without this both grow for as
// long as the process runs.
//
// Both jobs run on one ticker in one goroutine. They share a cadence, they are
// both cheap and idempotent, and a single lifecycle is easier to reason about
// at shutdown than two. A failure in one does not skip the other.
type Janitor struct {
	tokens   tokenCleaner
	limiter  *loginLimiter
	interval time.Duration
	wg       sync.WaitGroup
}

// tokenCleaner is the token store surface the janitor drives, narrow enough
// that a test can substitute a fake and assert the call happened.
type tokenCleaner interface {
	Cleanup(ctx context.Context) error
}

// NewJanitor returns a janitor for this handler's token store and login
// limiter. It lives on the handler because the limiter is the handler's own
// state.
func (h *Handler) NewJanitor(interval time.Duration) *Janitor {
	return &Janitor{
		tokens:   normalizeTokenCleaner(h.tokenStore),
		limiter:  &h.limiter,
		interval: interval,
	}
}

// normalizeTokenCleaner collapses a nil *TokenStore into a nil interface. An
// interface holding a nil pointer is itself non-nil, so without this the
// janitor's nil check would pass and Cleanup would panic on the nil pool.
func normalizeTokenCleaner(s *TokenStore) tokenCleaner {
	if s == nil {
		return nil
	}
	return s
}

// Run starts the cleanup loop. Blocks until ctx is cancelled.
func (j *Janitor) Run(ctx context.Context) {
	j.wg.Add(1)
	defer j.wg.Done()

	slog.Info("auth janitor started", "interval", j.interval)

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("auth janitor stopped")
			return
		case <-ticker.C:
			j.CleanOnce(ctx)
		}
	}
}

// Wait blocks until the cleanup goroutine has exited.
func (j *Janitor) Wait() {
	j.wg.Wait()
}

// CleanOnce runs a single cleanup pass (exposed for testing).
func (j *Janitor) CleanOnce(ctx context.Context) {
	// A database failure is logged and the sweep still runs: the two jobs are
	// independent and neither should be skipped because the other broke.
	if j.tokens != nil {
		if err := j.tokens.Cleanup(ctx); err != nil {
			slog.Error("auth janitor: token cleanup", "error", err)
		}
	}
	if evicted := j.limiter.sweep(time.Now()); evicted > 0 {
		slog.Debug("auth janitor: login limiter swept", "evicted", evicted)
	}
}
