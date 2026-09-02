package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRaceQueue(t *testing.T) (*Queue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &Queue{client: client}, mr
}

// TestTakePendingExactlyOnceUnderRacingConsumers proves the atomic Lua drain:
// N consumers race to drain the same user's pending list. Every message must
// be delivered exactly once — no duplicates, no losses.
func TestTakePendingExactlyOnceUnderRacingConsumers(t *testing.T) {
	q, _ := newRaceQueue(t)
	ctx := context.Background()

	const total = 60
	for i := 0; i < total; i++ {
		job := PendingJob{UserID: "u", Method: "sendMessage"}
		if err := q.PushPending(ctx, "shard1", "u", job, timeHour); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	received := make(map[string]int)
	var wg sync.WaitGroup
	for c := 0; c < 8; c++ {
		wg.Add(1)
		go func(consumer int) {
			defer wg.Done()
			for {
				jobs, err := q.TakePending(ctx, "shard1", "u")
				if err != nil {
					t.Errorf("consumer %d: %v", consumer, err)
					return
				}
				if len(jobs) == 0 {
					if users, _ := q.PendingUsers(ctx, "shard1"); len(users) == 0 {
						return // index clean -> nothing left anywhere
					}
					continue
				}
				mu.Lock()
				for range jobs {
					received["m"]++
				}
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()

	if received["m"] != total {
		t.Fatalf("delivered %d of %d messages under racing consumers", received["m"], total)
	}
}

var timeHour = time.Hour

// TestRegisterUserBotConcurrentConsistency hammers the CAS script with many
// goroutines assigning random bots to the same users. Afterwards each user
// must appear in EXACTLY ONE reverse index and it must match the routing hash.
func TestRegisterUserBotConcurrentConsistency(t *testing.T) {
	q, _ := newRaceQueue(t)
	ctx := context.Background()
	bots := []string{"main", "shard1", "shard2", "shard3"}

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for u := 0; u < 25; u++ {
				userID := fmt.Sprintf("user-%d", u)
				botID := bots[(worker+u)%len(bots)]
				if _, err := q.RegisterUserBot(ctx, userID, botID); err != nil {
					t.Errorf("register %s->%s: %v", userID, botID, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	for u := 0; u < 25; u++ {
		userID := fmt.Sprintf("user-%d", u)
		routed, err := q.GetUserBot(ctx, userID)
		if err != nil || routed == "" {
			t.Fatalf("user %s unmapped after concurrent writes: %q err=%v", userID, routed, err)
		}
		for _, bot := range bots {
			members, _ := q.UsersMappedTo(ctx, bot)
			found := containsString(members, userID)
			if bot == routed && !found {
				t.Fatalf("user %s routed to %s but missing from its reverse index", userID, bot)
			}
			if bot != routed && found {
				t.Fatalf("user %s routed to %s but stale entry exists in reverse index of %s", userID, routed, bot)
			}
		}
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestSkipWriteDoesNotDuplicateReverseIndex pins the skip-write contract at
// the storage layer: repeated identical registration leaves exactly one index
// membership behind.
func TestSkipWriteDoesNotDuplicateReverseIndex(t *testing.T) {
	q, _ := newRaceQueue(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		changed, err := q.RegisterUserBot(ctx, "42", "shard2")
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && changed {
			t.Fatal("duplicate write reported change=true")
		}
	}
	members, _ := q.UsersMappedTo(ctx, "shard2")
	if len(members) != 1 || members[0] != "42" {
		t.Fatalf("reverse index duplicated entries: %v", members)
	}
}
