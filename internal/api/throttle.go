package api

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

/*
How often one source may try to sign in.

Two reasons, and the second is the one that is easy to miss.

The obvious one is guessing. Sign-in is the only route that answers to somebody
with no session, and without a limit it answers as many times a second as a
script can ask.

The other is that verifying a password here costs 64 MiB of memory by design —
argon2id is deliberately expensive, which is what makes a stolen hash hard to
crack. Unauthenticated requests that each allocate 64 MiB are a way to exhaust
the machine without guessing anything at all, and the cost is paid before the
password is known to be wrong. Rate limiting sign-in is therefore a memory
control as much as a credential one.

Deliberately generous per source: a technician who mistypes a password four
times in a row is having a bad enough morning. What it stops is the thousandth
attempt, not the fourth.
*/
const (
	// loginAttemptsPerMinute is the sustained rate one source may sign in at.
	loginAttemptsPerMinute = 10
	// loginBurst is how many may arrive at once before pacing begins.
	loginBurst = 5
	// throttleIdle is how long a source is remembered after its last attempt.
	// Entries are evicted so that the map cannot be grown without bound by
	// attempting sign-in from many addresses.
	throttleIdle = 15 * time.Minute
)

// throttle limits attempts per source address.
//
// In-process and per-instance, which fits a single self-hosted binary. It is
// not a distributed rate limiter and does not pretend to be one; the thing it
// protects — this process's memory and this deployment's accounts — is
// per-instance too.
type throttle struct {
	mu      sync.Mutex
	perIP   map[string]*bucket
	limit   rate.Limit
	burst   int
	idle    time.Duration
	lastGC  time.Time
	nowFunc func() time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// newThrottle builds a throttle at the default sign-in rate.
func newThrottle() *throttle {
	return &throttle{
		perIP: map[string]*bucket{},
		limit: rate.Every(time.Minute / loginAttemptsPerMinute),
		burst: loginBurst,
		idle:  throttleIdle,
	}
}

func (t *throttle) now() time.Time {
	if t.nowFunc != nil {
		return t.nowFunc()
	}
	return time.Now()
}

// allow reports whether a source may make an attempt now.
func (t *throttle) allow(source string) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.evictLocked(now)

	b, ok := t.perIP[source]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(t.limit, t.burst)}
		t.perIP[source] = b
	}
	b.seen = now
	return b.lim.AllowN(now, 1)
}

// evictLocked drops sources that have gone quiet. Swept on use rather than by
// a goroutine: there is no lifecycle to own, and a throttle nobody is asking
// about does not need tidying.
func (t *throttle) evictLocked(now time.Time) {
	if now.Sub(t.lastGC) < t.idle {
		return
	}
	t.lastGC = now
	for source, b := range t.perIP {
		if now.Sub(b.seen) > t.idle {
			delete(t.perIP, source)
		}
	}
}

// limited wraps a handler so it refuses a source making too many attempts.
//
// 429 with Retry-After, so a well-behaved client waits rather than retrying
// into the same wall. The body says nothing about whether the account exists,
// for the same reason the sign-in failure does not.
func (s *Server) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Throttle.allow(s.Proxies.clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests,
				errBody("too many attempts; wait a minute and try again"))
			return
		}
		next(w, r)
	}
}
