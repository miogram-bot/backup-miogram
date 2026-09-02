package config

import "testing"

func TestParseHelpers(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"single", "shard1=@a_bot", map[string]string{"shard1": "@a_bot"}},
		{"multiple with spaces", " shard1=@a , shard2=@b ", map[string]string{"shard1": "@a", "shard2": "@b"}},
		{"missing username", "shard1", map[string]string{"shard1": ""}},
		{"ignores empties", ",,shard1=@a,,", map[string]string{"shard1": "@a"}},
	}
	for _, tc := range cases {
		got := parseHelpers(tc.raw)
		if len(got) != len(tc.want) {
			t.Errorf("%s: parseHelpers(%q) = %v, want %v", tc.name, tc.raw, got, tc.want)
			continue
		}
		for id, user := range tc.want {
			if got[id] != user {
				t.Errorf("%s: helper %s = %q, want %q", tc.name, id, got[id], user)
			}
		}
	}
}

func TestIsHelper(t *testing.T) {
	cases := []struct {
		botID, mainBotID string
		want             bool
	}{
		{"main", "main", false},
		{"shard1", "main", true},
		{"helper2", "helper2", false},
	}
	for _, tc := range cases {
		cfg := Config{BotID: tc.botID, MainBotID: tc.mainBotID}
		if got := cfg.IsHelper(); got != tc.want {
			t.Errorf("IsHelper(bot=%s, main=%s) = %v, want %v", tc.botID, tc.mainBotID, got, tc.want)
		}
	}
}

func TestLoadNormalizesAIMD(t *testing.T) {
	t.Setenv("BOT_TOKEN", "test-token")
	t.Setenv("AIMD_SEED", "1")
	t.Setenv("AIMD_FLOOR", "0")
	t.Setenv("AIMD_DECAY", "2")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AIMDFloor != 1 {
		t.Errorf("AIMDFloor = %v, want clamped to 1", cfg.AIMDFloor)
	}
	if cfg.AIMDSeed != cfg.AIMDFloor {
		t.Errorf("AIMDSeed = %v, want >= floor", cfg.AIMDSeed)
	}
	if cfg.AIMDDecay != 0.5 {
		t.Errorf("AIMDDecay = %v, want default 0.5 for out-of-range input", cfg.AIMDDecay)
	}
	if cfg.AIMDMax != 0 {
		t.Errorf("AIMDMax = %v, want 0 (feedback-driven, no manual ceiling)", cfg.AIMDMax)
	}
}
