package fleet

import (
	"testing"
	"time"

	"miogram/internal/config"
)

func testConfig() config.Config {
	return config.Config{
		BotID:           "main",
		MainBotID:       "main",
		BotUsername:     "miogram_bot",
		Helpers:         map[string]string{"shard1": "@miogram_shard1_bot", "shard2": "@miogram_shard2_bot", "shard3": "@miogram_shard3_bot"},
		HeartbeatTTL:    45 * time.Second,
		SuggestCooldown: 6 * time.Hour,
		PendingTTL:      48 * time.Hour,
	}
}

func TestPeakWindowActive(t *testing.T) {
	cases := []struct {
		name string
		spec string
		at   time.Time
		want bool
	}{
		{"empty spec is reactive only", "", time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC), false},
		{"inside window", "18-24", time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC), true},
		{"window end excluded", "18-24", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), false},
		{"before window", "18-24", time.Date(2026, 1, 1, 17, 59, 0, 0, time.UTC), false},
		{"wrap midnight inside", "21-02", time.Date(2026, 1, 2, 1, 30, 0, 0, time.UTC), true},
		{"wrap midnight outside", "21-02", time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC), false},
		{"invalid spec", "abc-def", time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		if got := peakWindowActive(tc.spec, tc.at); got != tc.want {
			t.Errorf("%s: peakWindowActive(%q) = %v, want %v", tc.name, tc.spec, got, tc.want)
		}
	}
}

func TestManagerModeTransitionsAndFallback(t *testing.T) {
	cfg := testConfig()
	cfg.PeakHours = ""
	m := NewManager(cfg, nil)
	m.RefreshRing()

	if m.Mode() != ModeOffPeak {
		t.Fatalf("initial mode = %s, want offpeak", m.Mode())
	}
	if m.IsActive(cfg.MainBotID) != true {
		t.Fatal("main bot must always be active")
	}
	if m.IsActive("shard1") {
		t.Fatal("helper must be inactive in off-peak mode")
	}

	m.mode.Store(ModePeak)
	if got := m.Mode(); got != ModePeak {
		t.Fatalf("mode = %s, want peak", got)
	}
	m.mode.Store(ModeOffPeak)

	// Reactive escalation through the flood source.
	calls := 0
	m.SetFloodSource(func() bool { calls++; return true })
	if !m.Flooded() || calls != 1 {
		t.Fatal("flood source not consulted")
	}
}
