package fleet

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newTestLimiter(cfg LimiterConfig) (*Limiter, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	l := NewLimiter(cfg)
	l.now = func() time.Time { return now }
	return l, &now
}

func TestLimiterAllowsUpToBudgetThenWaits(t *testing.T) {
	l, _ := newTestLimiter(LimiterConfig{Seed: 3, Floor: 1})
	for i := 0; i < 3; i++ {
		wait, err := l.reserve()
		if err != nil {
			t.Fatal(err)
		}
		if wait != 0 {
			t.Fatalf("send %d within budget waited %v", i+1, wait)
		}
	}
	if wait, _ := l.reserve(); wait <= 0 || wait > time.Second {
		t.Fatalf("4th send wait = %v, want (0,1s]", wait)
	}
}

func TestLimiterMultiplicativeDecrease(t *testing.T) {
	l, now := newTestLimiter(LimiterConfig{Seed: 20, Floor: 2, Decay: 0.5, Cooldown: 30 * time.Second})
	if got := l.Budget(); got != 20 {
		t.Fatalf("budget = %v, want 20", got)
	}
	pause := l.Penalize(5 * time.Second)
	if pause != 5*time.Second {
		t.Fatalf("pause = %v, want 5s", pause)
	}
	if got := l.Budget(); got != 10 {
		t.Fatalf("budget after one flood = %v, want 10", got)
	}
	l.Penalize(time.Second)
	l.Penalize(time.Second)
	l.Penalize(time.Second)
	if got := l.Budget(); got != 2 {
		t.Fatalf("budget should clamp at floor, got %v", got)
	}
	if !l.Flooded() {
		t.Fatal("limiter should report flooded right after penalize")
	}
	*now = now.Add(time.Minute)
	if l.Flooded() {
		t.Fatal("flood flag should expire after cooldown")
	}
}

func TestLimiterAdditiveIncrease(t *testing.T) {
	l, now := newTestLimiter(LimiterConfig{
		Seed: 4, Floor: 2, Increment: 2, Decay: 0.5, Cooldown: 10 * time.Second,
	})
	l.lastGrow = *now
	*now = now.Add(3 * time.Second)
	l.reserve() // one clean second elapsed -> exactly +Increment (no idle catch-up)
	if got := l.Budget(); got != 6 {
		t.Fatalf("budget = %v, want 6 (+2 for the first clean tick)", got)
	}
	l.lastGrow = *now
	*now = now.Add(1 * time.Second)
	l.reserve()
	if got := l.Budget(); got != 8 {
		t.Fatalf("budget = %v, want 8", got)
	}
}

func TestLimiterGrowthStopsDuringCooldown(t *testing.T) {
	l, now := newTestLimiter(LimiterConfig{
		Seed: 4, Floor: 2, Increment: 5, Decay: 0.5, Cooldown: 30 * time.Second,
	})
	l.Penalize(time.Second)
	budget := l.Budget()
	*now = now.Add(5 * time.Second)
	l.reserve()
	if got := l.Budget(); got != budget {
		t.Fatalf("budget changed during cooldown: %v -> %v", budget, got)
	}
}

func TestLimiterOptionalHardCap(t *testing.T) {
	l, now := newTestLimiter(LimiterConfig{Seed: 4, Floor: 2, Increment: 10, Max: 9, Cooldown: time.Second})
	l.lastGrow = *now
	*now = now.Add(10 * time.Second)
	l.reserve()
	if got := l.Budget(); got != 9 {
		t.Fatalf("budget = %v, want capped at 9", got)
	}
}

// TestLimiterSeedRateIsConfiguredSeed pins the conservative starting budget
// (spec: start at ~20 msg/s) before any feedback arrives.
func TestLimiterSeedRateIsConfiguredSeed(t *testing.T) {
	l, _ := newTestLimiter(LimiterConfig{Seed: 20, Floor: 2})
	if got := l.Budget(); got != 20 {
		t.Fatalf("seed budget = %v, want 20", got)
	}
	if l.Flooded() {
		t.Fatal("fresh limiter must not report flooded state")
	}
}

// TestLimiterNoCatchUpAfterLongIdle proves the "no idle catch-up bursts"
// requirement: after a long silent period the very first send adds at most
// ONE increment, never a multiple for the elapsed idle time.
func TestLimiterNoCatchUpAfterLongIdle(t *testing.T) {
	l, now := newTestLimiter(LimiterConfig{Seed: 5, Floor: 2, Increment: 3})
	l.lastGrow = *now
	*now = now.Add(10 * time.Minute) // long idle
	l.reserve()
	if got := l.Budget(); got != 8 {
		t.Fatalf("budget after idle = %v, want 8 (single +3 step, no catch-up)", got)
	}
}

// TestLimiterPenalizeReturnsServerMandatedPause verifies retry_after is passed
// through untouched so the transport can honour it exactly.
func TestLimiterPenalizeReturnsServerMandatedPause(t *testing.T) {
	l, _ := newTestLimiter(LimiterConfig{Seed: 30, Floor: 2, Decay: 0.25})
	if pause := l.Penalize(13 * time.Second); pause != 13*time.Second {
		t.Fatalf("pause = %v, want exact retry_after of 13s", pause)
	}
	if got := l.Budget(); got != 7.5 {
		t.Fatalf("budget after decay 0.25 = %v, want 7.5", got)
	}
}

// TestLimiterConcurrentAcquire hammers Acquire from many goroutines under the
// race detector; total admitted sends within one window must never exceed the
// current budget.
func TestLimiterConcurrentAcquire(t *testing.T) {
	l := NewLimiter(LimiterConfig{Seed: 50, Floor: 2, Max: 50}) // fast real-time window
	const workers = 40
	var wg sync.WaitGroup
	admitted := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := l.Acquire(ctx); err == nil {
				admitted <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(admitted)
	n := len(admitted)
	if n == 0 {
		t.Fatal("no worker acquired a slot")
	}
	if n > 51 { // seed 50 within the first second (+1 tolerance across tick)
		t.Fatalf("%d workers admitted within first window, budget only 50", n)
	}
}

func TestLimiterAcquireRespectsContext(t *testing.T) {
	l, _ := newTestLimiter(LimiterConfig{Seed: 1, Floor: 1})
	if _, err := l.reserve(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Acquire(ctx); err == nil {
		t.Fatal("Acquire should fail on cancelled context while window is full")
	}
}
