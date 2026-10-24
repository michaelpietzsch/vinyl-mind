package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := New(3, time.Minute)
	t0 := time.Unix(1_790_000_000, 0)
	for i := 0; i < 3; i++ {
		if !l.Allow("a", t0) {
			t.Fatalf("request %d within the capacity refused", i+1)
		}
	}
	if l.Allow("a", t0) {
		t.Fatal("fourth request in the same instant allowed")
	}
	if !l.Allow("b", t0) {
		t.Fatal("keys must not share a bucket")
	}
	if !l.Allow("a", t0.Add(20*time.Second)) || l.Allow("a", t0.Add(20*time.Second)) {
		t.Fatal("one token per 20 s must come back")
	}
}
