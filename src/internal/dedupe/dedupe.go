// Package dedupe provides an in-memory TTL store for detecting duplicate
// deliveries of the same activity.
//
// Webhook providers redeliver failed deliveries, and the same underlying
// activity can arrive more than once (e.g. a commit pushed to a second
// branch, or a manual redelivery with a fresh delivery ID). The store keys on
// stable event identity (see event.Event.DedupeKey) rather than delivery IDs,
// which providers regenerate on every redelivery.
package dedupe

import (
	"sync"
	"time"
)

// sweepThreshold triggers a cleanup of expired entries once the store grows
// past this many keys, keeping memory bounded by recent activity.
const sweepThreshold = 4096

// Store records keys with a TTL and reports whether a key was already
// recorded within that window.
type Store struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

// NewStore returns a store that considers keys seen for ttl. ttl must be
// positive.
func NewStore(ttl time.Duration) *Store {
	return &Store{ttl: ttl, seen: make(map[string]time.Time)}
}

// Seen records key and reports whether it had already been recorded within
// the TTL window (in which case the caller should drop the duplicate).
func (s *Store) Seen(key string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.seen[key]; ok && now.Sub(t) < s.ttl {
		return true
	}
	s.seen[key] = now
	if len(s.seen) >= sweepThreshold {
		for k, t := range s.seen {
			if now.Sub(t) >= s.ttl {
				delete(s.seen, k)
			}
		}
	}
	return false
}

// Len returns the number of currently recorded keys (for tests and
// diagnostics).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}
