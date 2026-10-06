package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("attempt %d within burst was refused", i+1)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait <= 0 || wait > time.Minute {
		t.Fatalf("4th attempt: ok=%v wait=%v, want refusal with a retry hint under a minute", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("keys must be independent")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("a token should have refilled after a minute")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("only one token should have refilled")
	}
}

func TestSweepKeepsMemoryBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(2, time.Second)
	l.now = func() time.Time { return now }
	for i := range 1000 {
		l.Allow(string(rune('a'+i%26)) + string(rune(i)))
	}
	now = now.Add(10 * time.Minute) // everything has refilled
	l.Allow("fresh")
	if n := len(l.buckets); n != 1 {
		t.Fatalf("%d buckets left after sweep, want 1", n)
	}
}
