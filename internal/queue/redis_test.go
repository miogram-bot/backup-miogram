package queue

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestQueue(t *testing.T) (*Queue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &Queue{client: client}, mr
}

// TestRegisterUserBotSkipWriteAndReverseIndex verifies the atomic CAS Lua:
// identical writes are skipped (return 0) and the reverse index always tracks
// the current owner exactly.
func TestRegisterUserBotSkipWriteAndReverseIndex(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	changed, err := q.RegisterUserBot(ctx, "111", "main")
	if err != nil || !changed {
		t.Fatalf("first registration: changed=%v err=%v, want true/nil", changed, err)
	}

	// Skip-write: same value reports no change and keeps state identical.
	changed, err = q.RegisterUserBot(ctx, "111", "main")
	if err != nil || changed {
		t.Fatalf("duplicate registration should be skipped, got changed=%v err=%v", changed, err)
	}
	members, _ := q.UsersMappedTo(ctx, "main")
	if len(members) != 1 {
		t.Fatalf("skip-write mutated reverse index: %v", members)
	}

	// Migration to a helper must move the reverse-index membership.
	if changed, err = q.RegisterUserBot(ctx, "111", "shard2"); err != nil || !changed {
		t.Fatalf("move to shard2: changed=%v err=%v", changed, err)
	}
	members, err = q.UsersMappedTo(ctx, "shard2")
	if err != nil || len(members) != 1 || members[0] != "111" {
		t.Fatalf("reverse index shard2 = %v err=%v", members, err)
	}
	members, _ = q.UsersMappedTo(ctx, "main")
	if len(members) != 0 {
		t.Fatalf("reverse index main should be empty, got %v", members)
	}
	if bot, _ := q.GetUserBot(ctx, "111"); bot != "shard2" {
		t.Fatalf("routing lookup = %q, want shard2", bot)
	}
}

func TestMoveUserBotBatchMigration(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	for _, id := range []string{"1", "2", "3"} {
		if _, err := q.RegisterUserBot(ctx, id, "shard1"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"1", "2", "3"} {
		changed, err := q.MoveUserBot(ctx, id, "main")
		if err != nil || !changed {
			t.Fatalf("move %s: changed=%v err=%v", id, changed, err)
		}
	}
	members, _ := q.UsersMappedTo(ctx, "main")
	if len(members) != 3 {
		t.Fatalf("after migration main holds %v", members)
	}
	members, _ = q.UsersMappedTo(ctx, "shard1")
	if len(members) != 0 {
		t.Fatalf("after migration shard1 holds %v", members)
	}
}

func TestPendingQueueRoundtripAndAtomicDrain(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	job := PendingJob{UserID: "42", Method: "sendMessage"}
	if err := q.PushPending(ctx, "shard1", "42", job, time.Hour); err != nil {
		t.Fatal(err)
	}
	users, err := q.PendingUsers(ctx, "shard1")
	if err != nil || len(users) != 1 {
		t.Fatalf("pending users = %v err=%v", users, err)
	}

	jobs, err := q.TakePending(ctx, "shard1", "42")
	if err != nil || len(jobs) != 1 || jobs[0].Method != "sendMessage" {
		t.Fatalf("drain = %+v err=%v", jobs, err)
	}
	// Drain cleans the index and empties the list.
	users, _ = q.PendingUsers(ctx, "shard1")
	if len(users) != 0 {
		t.Fatalf("index after drain = %v", users)
	}
	jobs, _ = q.TakePending(ctx, "shard1", "42")
	if len(jobs) != 0 {
		t.Fatalf("second drain returned %+v", jobs)
	}
}

func TestPopOldestPendingFIFOAndIndexCleanup(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	for _, m := range []string{"a", "b"} {
		if err := q.PushPending(ctx, "shard1", "7", PendingJob{UserID: "7", Method: m}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	first, err := q.PopOldestPending(ctx, "shard1", "7")
	if err != nil || first == nil || first.Method != "a" {
		t.Fatalf("first pop = %+v err=%v", first, err)
	}
	last, err := q.PopOldestPending(ctx, "shard1", "7")
	if err != nil || last == nil || last.Method != "b" {
		t.Fatalf("second pop = %+v err=%v", last, err)
	}
	empty, err := q.PopOldestPending(ctx, "shard1", "7")
	if err != nil || empty != nil {
		t.Fatalf("third pop should be empty, got %+v err=%v", empty, err)
	}
	users, _ := q.PendingUsers(ctx, "shard1")
	if len(users) != 0 {
		t.Fatalf("index after pops = %v, want clean", users)
	}
}

func TestRepushPendingPreservesMetadata(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	job := PendingJob{UserID: "9", FromUserID: "8", Method: "sendMessage", Attempts: 2, EnqueuedAt: 100}
	if err := q.RepushPending(ctx, "shard1", job, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := q.PopOldestPending(ctx, "shard1", "9")
	if err != nil || got == nil {
		t.Fatalf("pop after repush = %+v err=%v", got, err)
	}
	if got.Attempts != 2 || got.FromUserID != "8" {
		t.Fatalf("metadata lost: %+v", got)
	}
	if got.EnqueuedAt != 100 {
		t.Fatalf("caller-set EnqueuedAt not preserved: %d", got.EnqueuedAt)
	}

	// Zero timestamp gets a fresh value (retry requeue path).
	fresh := PendingJob{UserID: "10", Method: "sendMessage"}
	if err := q.RepushPending(ctx, "shard1", fresh, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err = q.PopOldestPending(ctx, "shard1", "10")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.EnqueuedAt <= 100 {
		t.Fatalf("fresh EnqueuedAt not set: %d", got.EnqueuedAt)
	}
}

func TestLeaderLockAcquireRenewRelease(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	ok, err := q.TryLeaderLock(ctx, "token-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first acquire ok=%v err=%v", ok, err)
	}
	// Second instance cannot take it.
	ok, _ = q.TryLeaderLock(ctx, "token-b", time.Minute)
	if ok {
		t.Fatal("second instance acquired held lease")
	}
	// Holder renews; impostor renew is rejected.
	renewed, err := q.RenewLeaderLock(ctx, "token-b", time.Minute)
	if err != nil || renewed {
		t.Fatalf("impostor renewed lease: %v err=%v", renewed, err)
	}
	renewed, err = q.RenewLeaderLock(ctx, "token-a", time.Minute)
	if err != nil || !renewed {
		t.Fatalf("holder renew failed: %v err=%v", renewed, err)
	}
	if err := q.ReleaseLeaderLock(ctx, "token-a"); err != nil {
		t.Fatal(err)
	}
	ok, err = q.TryLeaderLock(ctx, "token-c", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire after release failed: %v err=%v", ok, err)
	}
}

func TestOutboundQueueLen(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := q.EnqueueOutbound(ctx, "main", []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	n, err := q.OutboundQueueLen(ctx, "main")
	if err != nil || n != 3 {
		t.Fatalf("len = %d err=%v", n, err)
	}
}
