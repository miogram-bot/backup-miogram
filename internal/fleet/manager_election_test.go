package fleet

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"miogram/internal/config"
	"miogram/internal/queue"
)

// --- helpers -------------------------------------------------------------

func newSharedRedis(t *testing.T) *queue.Queue {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return queue.NewWithClient(client)
}

// fastFleetConfig returns a config with tiny timers so leadership and mode
// tests run in milliseconds instead of seconds. The lease is 300ms with a
// renewal every 100ms, so a crashed leader is detected well under a second.
func fastFleetConfig(botID string) config.Config {
	cfg := helperConfig(botID)
	cfg.LeaderLease = 300 * time.Millisecond
	cfg.ModeReconcile = 50 * time.Millisecond
	return cfg
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// --- leader election ------------------------------------------------------

// TestLeaderElectionSingleLeaderAndTakeover simulates two instances competing
// for the fleet lease: exactly one must win; when the winner "crashes" (its
// context is cancelled and it stops renewing), the survivor must take over
// within roughly one lease period.
func TestLeaderElectionSingleLeaderAndTakeover(t *testing.T) {
	q := newSharedRedis(t)

	a := NewManager(fastFleetConfig("main"), q)
	b := NewManager(fastFleetConfig("shard1"), q)

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go a.leaderElectionLoop(ctxA)

	waitFor(t, 2*time.Second, "instance A to acquire leadership", func() bool {
		return a.IsLeader()
	})
	if b.IsLeader() {
		t.Fatal("instance B must not lead while A holds the lease")
	}

	// B competes concurrently while A is still healthy.
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	go b.leaderElectionLoop(ctxB)

	time.Sleep(150 * time.Millisecond) // let B attempt acquisition at least once
	if b.IsLeader() {
		t.Fatal("B acquired leadership although A holds a live lease")
	}
	if !a.IsLeader() {
		t.Fatal("A lost leadership without crashing")
	}

	// Crash A: cancel its loop so it stops renewing the lease. The key must
	// expire (lease=300ms), after which B acquires leadership.
	cancelA()
	waitFor(t, 3*time.Second, "instance B to take over after A crash", func() bool {
		return b.IsLeader()
	})
	if a.IsLeader() {
		t.Fatal("cancelled instance must not report leadership")
	}
}

// TestEnterModeBroadcastsOverPubSub verifies that every follower receives the
// mode switch instantly via the fleet:control channel.
func TestEnterModeBroadcastsOverPubSub(t *testing.T) {
	q := newSharedRedis(t)
	ctx := context.Background()

	sub, err := q.SubscribeControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	mgr := NewManager(helperConfig("main"), q)
	mgr.enterMode(ctx, ModePeak)

	select {
	case msg, ok := <-sub.Channel():
		if !ok {
			t.Fatal("control channel closed unexpectedly")
		}
		if msg.Payload == "" || !contains(msg.Payload, `"event":"mode"`) || !contains(msg.Payload, `"value":"peak"`) {
			t.Fatalf("unexpected broadcast payload: %q", msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pub/sub broadcast received within timeout")
	}
	if mgr.Mode() != ModePeak {
		t.Fatalf("local mode = %s, want peak", mgr.Mode())
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestForcedModesOverrideSignals checks FLEET_MODE=normal|peak short-circuits
// the reactive signals and persists until changed.
func TestForcedModesOverrideSignals(t *testing.T) {
	q := newSharedRedis(t)
	ctx := context.Background()

	flooded := true // even active flooding must not override forced normal
	forcedNormalCfg := helperConfig("main")
	forcedNormalCfg.FleetMode = "normal"
	mgr := NewManager(forcedNormalCfg, q)
	mgr.SetFloodSource(func() bool { return flooded })

	if !mgr.evaluateMode(ctx) || mgr.Mode() != ModeOffPeak {
		t.Fatalf("forced normal ignored flood signal, mode=%s", mgr.Mode())
	}

	forcedPeakCfg := helperConfig("shard1")
	forcedPeakCfg.FleetMode = "peak"
	follower := NewManager(forcedPeakCfg, q)
	if !follower.evaluateMode(ctx) || follower.Mode() != ModePeak {
		t.Fatal("forced peak not applied")
	}
}

// --- off-peak consolidation -----------------------------------------------

// TestMigrateSkipsChattingUsersAndNotifiesOnce implements the active-chat
// protection contract of off-peak batch migration:
//   - users with chatting;* stay on their helper (deferred),
//   - free users move back to main,
//   - each moved user gets EXACTLY ONE return notification on main's queue.
func TestMigrateSkipsChattingUsersAndNotifiesOnce(t *testing.T) {
	q := newSharedRedis(t)
	ctx := context.Background()

	cfg := helperConfig("main")
	mgr := NewManager(cfg, q)
	mgr.SetChatFilter(func(_ context.Context, userID string) (bool, error) {
		return userID == "chatting-user", nil // only this one is mid-chat
	})

	for _, id := range []string{"free1", "free2", "chatting-user"} {
		if _, err := q.RegisterUserBot(ctx, id, "shard1"); err != nil {
			t.Fatal(err)
		}
	}
	// Pre-existing main mapping should not be touched nor re-notified.
	if _, err := q.RegisterUserBot(ctx, "already-main", "main"); err != nil {
		t.Fatal(err)
	}

	// Durable mirror must record exactly the users that actually moved.
	var durable []string
	var durMu sync.Mutex
	mgr.SetDurableAssign(func(_ context.Context, userID, botID string) error {
		durMu.Lock()
		defer durMu.Unlock()
		durable = append(durable, userID+"->"+botID)
		return nil
	})

	mgr.migrateHelpersToMain(ctx)

	durMu.Lock()
	defer durMu.Unlock()
	if len(durable) != 2 {
		t.Fatalf("durable assigns = %v, want exactly the two moved users", durable)
	}
	for _, rec := range durable {
		if !strings.HasSuffix(rec, "->main") || strings.Contains(rec, "chatting-user") {
			t.Errorf("unexpected durable assignment %q", rec)
		}
	}

	for _, id := range []string{"free1", "free2"} {
		if bot, _ := q.GetUserBot(ctx, id); bot != "main" {
			t.Fatalf("user %s not migrated, mapped to %q", id, bot)
		}
	}
	if bot, _ := q.GetUserBot(ctx, "chatting-user"); bot != "shard1" {
		t.Fatalf("chatting user was force-migrated to %q", bot)
	}
	if members, _ := q.UsersMappedTo(ctx, "shard1"); len(members) != 1 {
		t.Fatalf("reverse index shard1 = %v, want only chatting-user", members)
	}

	n, _ := q.OutboundQueueLen(ctx, "main")
	if n != 2 {
		t.Fatalf("return-to-main notifications = %d, want exactly 2 (one per moved user)", n)
	}
}

// --- observability ----------------------------------------------------------

// TestPublishStatsWritesObservableFields verifies the fleet:stats:<bot>
// snapshot consumed by dashboards.
func TestPublishStatsWritesObservableFields(t *testing.T) {
	q := newSharedRedis(t)
	ctx := context.Background()

	cfg := helperConfig("main")
	mgr := NewManager(cfg, q)
	mgr.mode.Store(ModePeak)
	mgr.isLeader.Store(true)
	budget := 17.5
	mgr.SetBudgetSource(func() float64 { return budget })
	mgr.SetFloodSource(func() bool { return true })

	mgr.publishStats(ctx)

	stats, err := q.Client().HGetAll(ctx, "fleet:stats:main").Result()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"budget":    "17.5",
		"flooded":   "true",
		"queue_len": "0",
		"mode":      ModePeak,
		"is_leader": "true",
	}
	for field, expected := range want {
		if stats[field] != expected {
			t.Errorf("stats[%s] = %q, want %q", field, stats[field], expected)
		}
	}
	if _, ok := stats["ts"]; !ok {
		t.Error("stats snapshot missing ts timestamp")
	}
}

// TestHeartbeatTTLExpiry proves that a bot which stops sending heartbeats is
// treated as inactive once its TTL lapses.
func TestHeartbeatTTLExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	q := queue.NewWithClient(client)

	ctx := context.Background()
	if err := q.BotHeartbeat(ctx, "shard1", time.Second); err != nil {
		t.Fatal(err)
	}
	if !q.BotHeartbeatAlive(ctx, "shard1") {
		t.Fatal("fresh heartbeat reported dead")
	}

	cfg := helperConfig("main")
	mgr := NewManager(cfg, q)
	mgr.mode.Store(ModePeak) // peak keeps helpers active only while heartbeats live
	if !mgr.IsActive("shard1") {
		t.Fatal("helper with live heartbeat must be active during peak")
	}

	mr.FastForward(2 * time.Second) // lapse the TTL
	if q.BotHeartbeatAlive(ctx, "shard1") {
		t.Fatal("expired heartbeat still alive")
	}
	if mgr.IsActive("shard1") {
		t.Fatal("resolver would route to a dead helper")
	}
}

// TestApplySharedModeReconcilesMissedBroadcast covers the polling fallback for
// followers that miss a pub/sub message; leaders must ignore the shared key
// because they are authoritative.
func TestApplySharedModeReconcilesMissedBroadcast(t *testing.T) {
	q := newSharedRedis(t)
	ctx := context.Background()

	follower := NewManager(helperConfig("shard1"), q)
	if err := q.SetFleetMode(ctx, ModePeak); err != nil {
		t.Fatal(err)
	}
	follower.applySharedMode(ctx)
	if follower.Mode() != ModePeak {
		t.Fatalf("follower did not reconcile, mode=%s", follower.Mode())
	}

	leader := NewManager(helperConfig("main"), q)
	leader.isLeader.Store(true)
	leader.mode.Store(ModeOffPeak)
	leader.applySharedMode(ctx) // shared says peak; leader must keep its own state
	if leader.Mode() != ModeOffPeak {
		t.Fatal("leader must not follow its own published state backwards")
	}
}
