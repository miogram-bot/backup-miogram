package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"miogram/internal/config"
	"miogram/internal/fleet"
	"miogram/internal/queue"
	"miogram/internal/storage"
)

// fakeRouting is an in-memory routingBackend standing in for PostgreSQL so
// the resolver's two-tier read path is testable without a live database.
type fakeRouting struct {
	assigned map[string]string
	pgReads  int
	pgWrites []string // "user->bot" records in call order
	failRead bool
}

func newFakeRouting() *fakeRouting {
	return &fakeRouting{assigned: map[string]string{}}
}

func (f *fakeRouting) AssignedBot(_ context.Context, userID string) (string, error) {
	f.pgReads++
	if f.failRead {
		return "", errors.New("pg unavailable")
	}
	bot, ok := f.assigned[userID]
	if !ok {
		return "", pgx.ErrNoRows
	}
	return bot, nil
}

func (f *fakeRouting) UpdateAssignedBot(_ context.Context, userID, botID string) error {
	f.pgWrites = append(f.pgWrites, userID+"->"+botID)
	f.assigned[userID] = botID
	return nil
}

// fakeStore is an in-memory userCreator standing in for PostgreSQL.
type fakeStore struct {
	mu    sync.Mutex
	users map[string]storage.User
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]storage.User{}}
}

func (f *fakeStore) CreateUser(_ context.Context, userID, _ string, _ int64) (storage.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := storage.User{UserID: userID, UniqID: userID}
	f.users[userID] = u
	return u, nil
}

func (f *fakeStore) UserByID(_ context.Context, userID string) (storage.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return storage.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func newResolverHarness(t *testing.T, mode string) (*Service, *fleet.Manager, *fakeRouting, *fakeStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	q := queue.NewWithClient(rdb)

	cfg := config.Config{
		BotID:           "main",
		MainBotID:       "main",
		BotUsername:     "miogram_bot",
		SuggestCooldown: time.Hour,
		MigrationNotify: true,
		Helpers: map[string]string{
			"shard1": "@miogram_shard1_bot",
			"shard2": "@miogram_shard2_bot",
			"shard3": "@miogram_shard3_bot",
		},
	}
	mgr := fleet.NewManager(cfg, q)
	if mode == fleet.ModePeak {
		mgr.SetLocalMode(fleet.ModePeak)
	}
	fake := newFakeRouting()
	store := newFakeStore()
	svc := &Service{cfg: cfg, redis: q, fleet: mgr, routing: fake, userStore: store}
	return svc, mgr, fake, store, mr
}

// TestResolveDeliveryTable walks the complete three-tier read path:
//
//	Redis HIT, active helper          -> helper            (Case B)
//	Redis HIT, main                   -> main              (Case A)
//	Redis MISS -> PG hit, active      -> helper + redis backfill
//	Redis MISS -> PG hit, off-peak    -> main fallback persisted in BOTH stores
//	Redis MISS -> PG row missing      -> error ("user does not exist")
//	Redis MISS -> PG empty (legacy)   -> main + durable backfill
func TestResolveDeliveryTable(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		redisMapTo  string
		pgAssigned  string
		pgRowExists bool
		helperBeat  bool
		want        string
		wantErr     bool
	}{
		{
			name:        "case A: registered user never fleet-assigned routes via main",
			mode:        fleet.ModePeak,
			pgRowExists: true, // assigned_bot = "" in PG
			want:        "main",
		},
		{
			name: "case B: active helper receives directly",
			mode: fleet.ModePeak, redisMapTo: "shard1", helperBeat: true,
			want: "shard1",
		},
		{
			name: "tier 2: pg hit backfills redis and routes",
			mode: fleet.ModePeak, pgAssigned: "shard2", pgRowExists: true, helperBeat: true,
			want: "shard2",
		},
		{
			name: "tier 2: inactive helper falls back and persists to both stores",
			mode: fleet.ModeOffPeak, pgAssigned: "shard3", pgRowExists: true,
			want: "main",
		},
		{
			name:        "brand new user is created and routed to main",
			mode:        fleet.ModePeak,
			pgRowExists: false,
			want:        "main",
		},
		{
			name:        "legacy row with empty assignment backfills main durably",
			mode:        fleet.ModePeak,
			pgRowExists: true,
			want:        "main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, fake, store, _ := newResolverHarness(t, tc.mode)
			ctx := context.Background()

			if tc.redisMapTo != "" {
				if _, err := svc.redis.RegisterUserBot(ctx, "rcpt", tc.redisMapTo); err != nil {
					t.Fatal(err)
				}
			}
			if tc.pgRowExists {
				fake.assigned["rcpt"] = tc.pgAssigned
			}
			if tc.helperBeat && tc.pgAssigned != "" {
				if err := svc.redis.BotHeartbeat(ctx, tc.pgAssigned, time.Minute); err != nil {
					t.Fatal(err)
				}
			} else if tc.helperBeat && tc.redisMapTo != "" {
				if err := svc.redis.BotHeartbeat(ctx, tc.redisMapTo, time.Minute); err != nil {
					t.Fatal(err)
				}
			}

			got, err := svc.resolveDelivery(ctx, "rcpt")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "does not exist") {
					t.Fatalf("want non-existent error, got %q, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveDelivery() = %q, want %q", got, tc.want)
			}
			if !tc.wantErr && !tc.pgRowExists && tc.redisMapTo == "" && tc.pgAssigned == "" {
				if _, ok := store.users["rcpt"]; !ok {
					t.Fatalf("brand-new user rcpt was not created in the store")
				}
			}
		})
	}
}

// TestResolveFallbackPersistsBothStores verifies Case C fallback durability:
// after falling back from an inactive helper, Redis AND PostgreSQL both point
// at main, and the move-back notice cooldown marker exists exactly once.
func TestResolveFallbackPersistsBothStores(t *testing.T) {
	svc, _, fake, _, _ := newResolverHarness(t, fleet.ModeOffPeak)
	ctx := context.Background()

	if _, err := svc.registerBotForUser(ctx, "rcpt", "shard1"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		got, err := svc.resolveDelivery(ctx, "rcpt")
		if err != nil || got != "main" {
			t.Fatalf("resolution %d = %q err=%v", i+1, got, err)
		}
	}
	if bot, _ := svc.redis.GetUserBot(ctx, "rcpt"); bot != "main" {
		t.Fatalf("redis routing = %q, want main", bot)
	}
	if fake.assigned["rcpt"] != "main" {
		t.Fatalf("pg assigned_bot = %q, want main", fake.assigned["rcpt"])
	}
	n, _ := svc.redis.Client().Exists(ctx, "fleet:movednotice:rcpt").Result()
	if n != 1 {
		t.Fatalf("expected single notification cooldown marker, exists=%d", n)
	}
}

// TestRegisterBotForUserSkipsDurableWriteOnNoChange pins the storage rule:
// identical mappings perform zero PostgreSQL writes after the first one.
func TestRegisterBotForUserSkipsDurableWriteOnNoChange(t *testing.T) {
	svc, _, fake, _, _ := newResolverHarness(t, fleet.ModeOffPeak)
	ctx := context.Background()

	changed, err := svc.registerBotForUser(ctx, "u1", "shard2")
	if err != nil || !changed || len(fake.pgWrites) != 1 {
		t.Fatalf("first registration changed=%v pgWrites=%v err=%v", changed, fake.pgWrites, err)
	}
	for i := 0; i < 4; i++ {
		changed, err := svc.registerBotForUser(ctx, "u1", "shard2")
		if err != nil || changed {
			t.Fatalf("duplicate registration %d reported change: %v %v", i, changed, err)
		}
	}
	if len(fake.pgWrites) != 1 {
		t.Fatalf("skip-write must avoid durable writes, got %d", len(fake.pgWrites))
	}
}

// TestSendFailsWhenStoreUnavailable ensures that when no store is configured a
// brand-new recipient cannot be created, so send() fails loudly instead of
// silently dropping the message or touching Telegram.
func TestSendFailsWhenStoreUnavailable(t *testing.T) {
	svc, _, _, _, _ := newResolverHarness(t, fleet.ModePeak) // tg intentionally nil
	svc.userStore = nil

	resp, err := svc.send(context.Background(), "sendMessage",
		map[string]any{"chat_id": "999999", "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "cannot create") {
		t.Fatalf("want cannot-create error, got resp=%+v err=%v", resp, err)
	}
	if resp.Ok {
		t.Fatal("response must be zero-valued on resolution failure")
	}
}

// TestUserIsChattingGuard pins the active-chat detection used by bounce and
// migration paths; unknown users are treated as free to migrate.
func TestUserIsChattingGuard(t *testing.T) {
	if (&Service{}).userIsChatting(context.Background(), "") {
		t.Fatal("empty user must not count as chatting")
	}
}

// TestResolveDeliveryRegistersCurrentBotNotMain pins the fix for the
// user_bot_routing registration bug: when a non-main instance receives the
// first message from a brand-new user, the routing entry must use that
// instance's bot ID, not "main".
func TestResolveDeliveryRegistersCurrentBotNotMain(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	q := queue.NewWithClient(rdb)
	cfg := config.Config{
		BotID:           "shard1",
		MainBotID:       "main",
		BotUsername:     "miogram_shard1_bot",
		SuggestCooldown: time.Hour,
		Helpers:         map[string]string{"shard1": "@miogram_shard1_bot"},
	}
	mgr := fleet.NewManager(cfg, q)
	mgr.SetLocalMode(fleet.ModePeak)
	fake := newFakeRouting()
	store := newFakeStore()
	svc := &Service{cfg: cfg, redis: q, fleet: mgr, routing: fake, userStore: store}
	// Mark the shard as active so deliverVia keeps it instead of falling back.
	if err := svc.redis.BotHeartbeat(context.Background(), "shard1", time.Minute); err != nil {
		t.Fatal(err)
	}

	got, err := svc.resolveDelivery(context.Background(), "rcpt")
	if err != nil {
		t.Fatalf("resolveDelivery: %v", err)
	}
	if got != "shard1" {
		t.Fatalf("resolved bot = %q, want shard1", got)
	}
	if bot, _ := svc.redis.GetUserBot(context.Background(), "rcpt"); bot != "shard1" {
		t.Fatalf("redis routing = %q, want shard1", bot)
	}
	if len(fake.pgWrites) != 1 || fake.pgWrites[0] != "rcpt->shard1" {
		t.Fatalf("pg assigned_bot writes = %v, want [rcpt->shard1]", fake.pgWrites)
	}
}
