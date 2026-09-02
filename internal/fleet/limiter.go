package fleet

import (
	"context"
	"math"
	"sync"
	"time"
)

// LimiterConfig seeds the adaptive limiter. Max == 0 means there is no manual
// ceiling: the budget grows until Telegram itself answers with 429/flood_wait,
// which multiplicatively decreases it (AIMD).
type LimiterConfig struct {
	Seed      float64       // starting budget, msgs/sec
	Floor     float64       // lowest allowed budget
	Increment float64       // additive increase per second without floods
	Decay     float64       // multiplicative decrease on flood (0<d<1)
	Max       float64       // optional hard ceiling; 0 disables it
	Cooldown  time.Duration // how long a flood event blocks growth and marks the bot as flooded
}

// Limiter is an adaptive sliding-window rate limiter implementing
// additive-increase / multiplicative-decrease around Telegram's feedback.
// It guards a single outbound consumer goroutine per bot token; every method
// is safe for concurrent use.
type Limiter struct {
	cfg LimiterConfig

	mu           sync.Mutex
	budget       float64
	slots        []time.Time // send timestamps inside the trailing second
	lastGrow     time.Time
	lastFlood    time.Time
	floodedUntil time.Time

	now func() time.Time // injectable clock for tests
}

func NewLimiter(cfg LimiterConfig) *Limiter {
	if cfg.Floor < 0.1 {
		cfg.Floor = 0.1
	}
	if cfg.Seed < cfg.Floor {
		cfg.Seed = cfg.Floor
	}
	if cfg.Increment <= 0 {
		cfg.Increment = 1
	}
	if cfg.Decay <= 0 || cfg.Decay >= 1 {
		cfg.Decay = 0.5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	now := time.Now
	return &Limiter{
		cfg:    cfg,
		budget: cfg.Seed,
		now:    now,
	}
}

// Acquire blocks until one send slot is available or ctx is done.
func (l *Limiter) Acquire(ctx context.Context) error {
	for {
		wait, err := l.reserve()
		if err != nil {
			return err
		}
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *Limiter) reserve() (time.Duration, error) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	l.grow(now)
	allowed := int(math.Floor(l.budget))
	if allowed < 1 {
		allowed = 1
	}
	if len(l.slots) < allowed {
		l.slots = append(l.slots, now)
		return 0, nil
	}
	return l.slots[0].Add(time.Second).Sub(now), nil
}

// Penalize applies multiplicative decrease after a 429/flood_wait response and
// returns how long sending must pause (the server-provided retry_after).
func (l *Limiter) Penalize(retryAfter time.Duration) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.budget = math.Max(l.cfg.Floor, l.budget*l.cfg.Decay)
	if l.cfg.Max > 0 {
		l.budget = math.Min(l.budget, l.cfg.Max)
	}
	l.lastFlood = now
	cooldown := retryAfter + l.cfg.Cooldown
	if cooldown < l.cfg.Cooldown {
		cooldown = l.cfg.Cooldown
	}
	l.floodedUntil = now.Add(cooldown)
	pause := retryAfter
	if pause < 0 {
		pause = 0
	}
	return pause
}

// Flooded reports whether recent traffic hit Telegram limits; the fleet uses
// this as a reactive peak signal.
func (l *Limiter) Flooded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.now().Before(l.floodedUntil)
}

// Budget exposes the current adaptive rate (for monitoring).
func (l *Limiter) Budget() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.budget
}

func (l *Limiter) prune(now time.Time) {
	cutoff := now.Add(-time.Second)
	idx := sortSlots(l.slots, cutoff)
	if idx > 0 {
		l.slots = l.slots[idx:]
	}
}

func (l *Limiter) grow(now time.Time) {
	if !l.lastFlood.IsZero() && now.Sub(l.lastFlood) < l.cfg.Cooldown {
		return
	}
	if l.lastGrow.IsZero() {
		l.lastGrow = now
		return
	}
	// Additive increase: one increment per second of clean sending, never a
	// catch-up burst after idle, so the budget converges gradually onto the
	// true ceiling discovered through 429 feedback.
	if now.Sub(l.lastGrow) < time.Second {
		return
	}
	l.budget += l.cfg.Increment
	if l.cfg.Max > 0 && l.budget > l.cfg.Max {
		l.budget = l.cfg.Max
	}
	l.lastGrow = now
}

func sortSlots(slots []time.Time, cutoff time.Time) int {
	idx := 0
	for idx < len(slots) && slots[idx].Before(cutoff) {
		idx++
	}
	return idx
}
