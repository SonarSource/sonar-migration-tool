// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A nil gate and a non-positive interval must never block.
func TestSubmitGateDisabled(t *testing.T) {
	var nilGate *submitGate
	start := time.Now()
	if err := nilGate.wait(context.Background(), nil); err != nil {
		t.Fatalf("nil gate returned error: %v", err)
	}
	for _, iv := range []time.Duration{0, -time.Second} {
		g := newSubmitGate(iv)
		if err := g.wait(context.Background(), nil); err != nil {
			t.Fatalf("gate(%v) returned error: %v", iv, err)
		}
		if err := g.wait(context.Background(), nil); err != nil {
			t.Fatalf("gate(%v) second wait returned error: %v", iv, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("disabled gates should not block, took %v", elapsed)
	}
}

// The first wait is immediate; the second is delayed by ~minInterval.
func TestSubmitGateSpacesConsecutiveCalls(t *testing.T) {
	const iv = 60 * time.Millisecond
	g := newSubmitGate(iv)

	start := time.Now()
	if err := g.wait(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if first := time.Since(start); first > iv/2 {
		t.Fatalf("first wait should be immediate, took %v", first)
	}

	mid := time.Now()
	if err := g.wait(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if gap := time.Since(mid); gap < iv-10*time.Millisecond {
		t.Fatalf("second wait should be delayed by ~%v, was %v", iv, gap)
	}
}

// Context cancellation while waiting returns promptly with the ctx error.
func TestSubmitGateRespectsContext(t *testing.T) {
	g := newSubmitGate(10 * time.Second)
	if err := g.wait(context.Background(), nil); err != nil { // arm "last"
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := g.wait(ctx, nil); err == nil {
		t.Fatal("expected context error, got nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled wait should return promptly, took %v", elapsed)
	}
}

// Concurrent callers are serialized: N calls through a gate of interval iv
// take at least (N-1)*iv (the first is immediate, each subsequent is spaced).
func TestSubmitGateSerializesConcurrentCallers(t *testing.T) {
	const (
		iv = 20 * time.Millisecond
		n  = 5
	)
	g := newSubmitGate(iv)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.wait(context.Background(), nil)
		}()
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed < time.Duration(n-1)*iv {
		t.Fatalf("expected >= %v for %d spaced submits, took %v", time.Duration(n-1)*iv, n, elapsed)
	}
}
