// Package server — per-caller rate limiting.
//
// The limiter is an in-process token bucket keyed on `caller.ID`. It
// assumes the auth middleware has already stamped a caller on the
// request context — anonymous callers share a single bucket keyed on
// the literal id "anonymous". When the limiter is disabled the
// middleware returns a no-op; otherwise it allocates one rate.Limiter
// per caller id on demand.
//
// Bucket eviction is intentionally not implemented: bearer-token mode is
// bounded by the tokens file, so the registry cannot grow beyond the
// number of configured principals plus "anonymous". Trusted-header mode
// can see unbounded caller ids in principle; if that becomes the deployed
// shape, add a background GC keyed on last-seen time. See CHANGELOG
// 0.4.1 for the deferred note.

package server

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// rateLimiter is a thread-safe registry of per-caller token buckets.
type rateLimiter struct {
	rps   rate.Limit
	burst int

	mu      sync.Mutex
	buckets map[string]*rate.Limiter
}

func newRateLimiter(cfg config.RateLimitConfig) *rateLimiter {
	return &rateLimiter{
		rps:     rate.Limit(cfg.RPS),
		burst:   cfg.Burst,
		buckets: make(map[string]*rate.Limiter),
	}
}

// limiterFor returns (or creates) the rate.Limiter for the given caller id.
func (l *rateLimiter) limiterFor(id string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if lim, ok := l.buckets[id]; ok {
		return lim
	}
	lim := rate.NewLimiter(l.rps, l.burst)
	l.buckets[id] = lim
	return lim
}

// retryAfterSeconds estimates how long to wait before the bucket will
// have a token available. Used to populate the Retry-After header.
// Falls back to "1" when the limiter can't give a useful reservation
// (e.g. rps is zero, which should be caught by config defaults).
func retryAfterSeconds(lim *rate.Limiter) int {
	reservation := lim.ReserveN(time.Now(), 1)
	defer reservation.Cancel() // don't actually consume the token
	if !reservation.OK() {
		return 1
	}
	delay := reservation.Delay()
	if delay <= 0 {
		return 1
	}
	secs := int(delay.Seconds() + 0.5)
	if secs < 1 {
		return 1
	}
	return secs
}

// rateLimitMiddleware applies per-caller token buckets to every request
// the middleware wraps. When cfg.Enabled is false it returns a no-op.
//
// The limiter sits after the auth middleware so it can key on the
// resolved caller id. Callers who trip the limit get a 429 with the
// Phase-3 structured error envelope plus a Retry-After header.
func rateLimitMiddleware(cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	if !cfg.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}
	rl := newRateLimiter(cfg)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := auth.CallerFromContext(r.Context())
			id := caller.ID
			if id == "" {
				id = "anonymous"
			}
			lim := rl.limiterFor(id)
			if !lim.Allow() {
				retry := retryAfterSeconds(lim)
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				writeError(w, http.StatusTooManyRequests,
					fmt.Errorf("%w: rate limit exceeded", model.ErrRateLimited))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
