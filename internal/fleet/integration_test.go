package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"miogram/internal/config"
	"miogram/internal/queue"
	"miogram/internal/telegram"
)

// newFleetRedis spins up one shared Redis simulating the fleet's shared state
// layer; every "instance" in these tests points at it.
func newFleetRedis(t *testing.T) *queue.Queue {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return queue.NewWithClient(client)
}

func helperConfig(botID string) config.Config {
	return config.Config{
		BotID:           botID,
		MainBotID:       "main",
		BotUsername:     "miogram_bot",
		Helpers:         map[string]string{"shard1": "@miogram_shard1_bot", "shard2": "@miogram_shard2_bot", "shard3": "@miogram_shard3_bot"},
		FleetMode:       "auto",
		PeakQueueLen:    200,
		OffPeakAfter:    10 * time.Minute,
		PendingTTL:      time.Hour,
		PendingFallback: 15 * time.Minute,
		PendingAttempts: 4,
		SuggestCooldown: time.Hour,
		MigrationNotify: true,
		HeartbeatTTL:    time.Minute,
	}
}

// TestCrossBotMigrationScenario walks the full lifecycle of a moved recipient:
//
//	main registers sender, shard1 registers recipient (peak migration)
//	-> resolver sees shard1 inactive after off-peak switch and falls back
//	-> messages sent while recipient had not started land in pending
//	-> timeout sweeper retries via main; NeedsStart requeues; success drains
//	-> permanent failure drops and notifies the original sender.
func TestCrossBotMigrationScenario(t *testing.T) {
	q := newFleetRedis(t)
	ctx := context.Background()

	mainCfg := helperConfig("main")
	helperCfg := helperConfig("shard1")
	main := NewManager(mainCfg, q)
	helper := NewManager(helperCfg, q)

	senderID, recipientID := "1000", "2000"

	// Peak: both users interact; consistent hashing assigns the recipient to
	// a helper and confirmation writes the routing entry (Registrar).
	main.mode.Store(ModePeak)
	if _, err := q.RegisterUserBot(ctx, senderID, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RegisterUserBot(ctx, recipientID, "shard1"); err != nil {
		t.Fatal(err)
	}

	// Off-peak: leader deactivates helpers; resolver must treat shard1 as
	// inactive and fall back to main.
	q.SetFleetMode(ctx, ModeOffPeak)
	if helper.IsActive("shard1") {
		t.Fatal("shard1 must be inactive off-peak")
	}
	if !main.IsActive("main") {
		t.Fatal("main must always be active")
	}

	// Resolver fallback (service-side move) reroutes the recipient to main.
	changed, err := q.MoveUserBot(ctx, recipientID, "main")
	if err != nil || !changed {
		t.Fatalf("resolver fallback move changed=%v err=%v", changed, err)
	}

	// A chat message arrives for the recipient while they are mapped to
	// shard1 but never started it: Case B queues it on shard1 with sender info.
	params := map[string]any{"chat_id": recipientID, "text": "hello from main"}
	if err := helper.EnqueuePending(ctx, "shard1", recipientID, senderID, "sendMessage", params); err != nil {
		t.Fatal(err)
	}
	users, _ := q.PendingUsers(ctx, "shard1")
	if len(users) != 1 || users[0] != recipientID {
		t.Fatalf("pending index = %v", users)
	}

	// Case C timeout: force staleness, then sweep. Each instance owns its own
	// sweeper, so the fallback deliverer is wired on the helper (the bot that
	// queued the message). First attempt fails with NeedsStart -> requeue.
	var attempts int
	helper.SetFallbackDeliverer(func(_ context.Context, _, _ string, _ map[string]any) (telegram.APIResponse, error) {
		attempts++
		if attempts < 2 {
			return telegramNeedsStart(), nil
		}
		return telegramOK(), nil
	})
	staleJob, err := q.PopOldestPending(ctx, "shard1", recipientID)
	if err != nil || staleJob == nil {
		t.Fatalf("pop stale job: %+v err=%v", staleJob, err)
	}
	staleJob.EnqueuedAt = time.Now().Add(-2 * helperCfg.PendingFallback).Unix()
	if err := q.RepushPending(ctx, "shard1", *staleJob, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := helper.sweepPending(ctx, "shard1"); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("fallback attempts = %d, want 1 (requeued as NeedsStart)", attempts)
	}
	parked, err := q.PopOldestPending(ctx, "shard1", recipientID)
	if err != nil || parked == nil || parked.Attempts != 1 {
		t.Fatalf("requeued job = %+v err=%v", parked, err)
	}

	// Second attempt succeeds: message delivered through main, queue empty.
	// The successful requeue rearmed the timestamp, so age it again.
	parked.EnqueuedAt = time.Now().Add(-2 * helperCfg.PendingFallback).Unix()
	if err := q.RepushPending(ctx, "shard1", *parked, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := helper.sweepPending(ctx, "shard1"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("total fallback attempts = %d, want 2", attempts)
	}
	left, _ := q.PendingUsers(ctx, "shard1")
	if len(left) != 0 {
		t.Fatalf("pending should be drained, left %v", left)
	}
}

func TestSweepDropsAndNotifiesSenderOnPermanentFailure(t *testing.T) {
	q := newFleetRedis(t)
	ctx := context.Background()

	cfg := helperConfig("shard1")
	mgr := NewManager(cfg, q)
	if err := mgr.EnqueuePending(ctx, "shard1", "2000", "1000", "sendMessage",
		map[string]any{"chat_id": "2000", "text": "x"}); err != nil {
		t.Fatal(err)
	}
	stale, _ := q.PopOldestPending(ctx, "shard1", "2000")
	stale.EnqueuedAt = time.Now().Add(-2 * cfg.PendingFallback).Unix()
	if err := q.RepushPending(ctx, "shard1", *stale, time.Hour); err != nil {
		t.Fatal(err)
	}

	calls := 0
	mgr.SetFallbackDeliverer(func(context.Context, string, string, map[string]any) (telegram.APIResponse, error) {
		calls++
		return telegramBlocked(), nil
	})
	if err := mgr.sweepPending(ctx, "shard1"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("deliverer calls = %d", calls)
	}
	left, _ := q.PendingUsers(ctx, "shard1")
	if len(left) != 0 {
		t.Fatalf("permanently undeliverable job must be dropped, left %v", left)
	}
	// Sender notification was enqueued onto the main bot's outbound queue.
	n, err := q.OutboundQueueLen(ctx, "main")
	if err != nil || n != 1 {
		t.Fatalf("sender notification queue len = %d err=%v, want 1", n, err)
	}
}

// TestEvaluateModeLoadSignals checks busy/quiet transitions incl. sustained
// quiet requirement before downgrading.
func TestEvaluateModeLoadSignals(t *testing.T) {
	q := newFleetRedis(t)
	ctx := context.Background()

	flooded := false
	queueLen := int64(0)
	cfg := helperConfig("main")
	cfg.PeakHours = "" // reactive only
	mgr := NewManager(cfg, q)
	mgr.SetFloodSource(func() bool { return flooded })
	_ = queueLen // queue length read live from redis below

	// Busy via flood signal -> peak.
	flooded = true
	if !mgr.evaluateMode(ctx) || mgr.Mode() != ModePeak {
		t.Fatalf("mode = %s, want peak under flood", mgr.Mode())
	}
	// Quiet but within sustained window: stays peak.
	flooded = false
	mgr.lastBusy.Store(time.Now().Unix())
	if !mgr.evaluateMode(ctx) || mgr.Mode() != ModePeak {
		t.Fatalf("downgraded before sustained quiet period")
	}
	// Quiet beyond OffPeakAfter -> off-peak.
	mgr.lastBusy.Store(time.Now().Add(-cfg.OffPeakAfter - time.Minute).Unix())
	if !mgr.evaluateMode(ctx) || mgr.Mode() != ModeOffPeak {
		t.Fatalf("mode = %s, want off-peak after quiet period", mgr.Mode())
	}

	// Forced modes short-circuit signals.
	forced := helperConfig("main")
	forced.FleetMode = "peak"
	fm := NewManager(forced, q)
	if !fm.evaluateMode(ctx) || fm.Mode() != ModePeak {
		t.Fatal("forced peak ignored")
	}
}

func telegramOK() telegram.APIResponse { return telegram.APIResponse{Ok: true} }

func telegramNeedsStart() telegram.APIResponse {
	return telegram.APIResponse{ErrorCode: 403, Description: "Forbidden: bot can't initiate conversation with a user"}
}

func telegramBlocked() telegram.APIResponse {
	return telegram.APIResponse{ErrorCode: 403, Description: "Forbidden: bot was blocked by the user"}
}
