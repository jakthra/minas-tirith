package dedupe

import (
	"testing"
	"time"
)

func TestSeen(t *testing.T) {
	s := NewStore(time.Minute)

	if s.Seen("a") {
		t.Error("first record of a key must not be a duplicate")
	}
	if !s.Seen("a") {
		t.Error("second record of a key must be a duplicate")
	}
	if s.Seen("b") {
		t.Error("first record of another key must not be a duplicate")
	}
	if s.Len() != 2 {
		t.Errorf("len = %d, want 2", s.Len())
	}
}

func TestSeenExpiresAfterTTL(t *testing.T) {
	s := NewStore(10 * time.Millisecond)

	if s.Seen("a") {
		t.Fatal("unexpected duplicate")
	}
	time.Sleep(15 * time.Millisecond)
	if s.Seen("a") {
		t.Error("key seen after TTL expiry must be treated as new")
	}
}

func TestSweepBoundsMemory(t *testing.T) {
	s := NewStore(time.Nanosecond) // everything expires immediately
	for i := 0; i < sweepThreshold+100; i++ {
		s.Seen(string(rune(i % 128)))
	}
	time.Sleep(time.Millisecond)
	s.Seen("final") // triggers the sweep
	if got := s.Len(); got > sweepThreshold {
		t.Errorf("len = %d, sweep should have dropped expired entries", got)
	}
}
