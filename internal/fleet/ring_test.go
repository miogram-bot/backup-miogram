package fleet

import "testing"

func TestRingAssignsStableHelpers(t *testing.T) {
	ring := NewRing(64)
	ring.Set([]string{"shard1", "shard2", "shard3"})

	first := map[string]string{}
	for _, id := range []string{"111", "222", "333", "444", "555"} {
		assign := ring.Assign(id)
		if assign == "" {
			t.Fatalf("no helper assigned for %s", id)
		}
		first[id] = assign
		if again := ring.Assign(id); again != assign {
			t.Errorf("user %s bounced between %s and %s", id, assign, again)
		}
	}
}

func TestRingRemovalMovesMinimalUsers(t *testing.T) {
	ring := NewRing(128)
	ring.Set([]string{"shard1", "shard2", "shard3"})
	users := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		users = append(users, "user"+itoa(i))
	}
	before := map[string]string{}
	for _, u := range users {
		before[u] = ring.Assign(u)
	}

	ring.Set([]string{"shard1", "shard2"})
	moved := 0
	for _, u := range users {
		if after := ring.Assign(u); after != before[u] {
			moved++
			if before[u] != "shard3" {
				t.Errorf("user %s moved from %s though its owner stayed", u, before[u])
			}
		}
	}
	if moved != 0 && moved > len(users)/3+50 {
		t.Errorf("removing one of three helpers moved %d/500 users; consistent hashing should move ~1/3", moved)
	}
}

func TestRingDistributionIsBalanced(t *testing.T) {
	ring := NewRing(160)
	ring.Set([]string{"a", "b", "c"})
	counts := map[string]int{}
	total := 3000
	for i := 0; i < total; i++ {
		counts[ring.Assign(itoa(i))]++
	}
	if len(counts) != 3 {
		t.Fatalf("expected all three helpers used, got %v", counts)
	}
	for id, n := range counts {
		if n < total/10 || n > total-total/10 {
			t.Errorf("helper %s got extreme share %d/%d", id, n, total)
		}
	}
}

// TestRingAdditionMovesOnlyShareToNewNode verifies the add direction of
// consistent hashing: when a 4th helper joins, only users whose hash now falls
// into the newcomer's arc move — and every moved user lands on the new bot.
func TestRingAdditionMovesOnlyShareToNewNode(t *testing.T) {
	ring := NewRing(128)
	ring.Set([]string{"shard1", "shard2", "shard3"})
	users := make([]string, 0, 900)
	for i := 0; i < 900; i++ {
		users = append(users, "u"+itoa(i))
	}
	before := make(map[string]string, len(users))
	for _, u := range users {
		before[u] = ring.Assign(u)
	}

	ring.Set([]string{"shard1", "shard2", "shard3", "shard4"})
	movedToNew, movedElsewhere := 0, 0
	for _, u := range users {
		if after := ring.Assign(u); after != before[u] {
			if after == "shard4" {
				movedToNew++
			} else {
				movedElsewhere++
			}
		}
	}
	if movedElsewhere != 0 {
		t.Errorf("%d existing users were remapped between old nodes on ADD", movedElsewhere)
	}
	// Expected share ~900/4 = 225; allow generous statistical slack.
	if movedToNew < 100 || movedToNew > 350 {
		t.Errorf("new node received %d users, want roughly 225", movedToNew)
	}
}

func TestRingEmpty(t *testing.T) {
	ring := NewRing(8)
	ring.Set(nil)
	if got := ring.Assign("42"); got != "" {
		t.Errorf("empty ring assigned %q", got)
	}
	ring.Set([]string{"only"})
	if got := ring.Assign("42"); got != "only" {
		t.Errorf("got %q, want only", got)
	}
}
