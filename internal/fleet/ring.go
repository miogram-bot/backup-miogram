package fleet

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"sync"
)

// Ring implements consistent hashing over helper bot IDs with virtual nodes.
// A user always maps to the same helper while the helper set is unchanged,
// which keeps migrations minimal when the ring membership changes.
type Ring struct {
	mu      sync.RWMutex
	vnodes  int
	points  []uint32
	owners  map[uint32]string
	members []string
}

func NewRing(vnodes int) *Ring {
	if vnodes < 1 {
		vnodes = 160
	}
	return &Ring{vnodes: vnodes, owners: map[uint32]string{}}
}

// Set replaces the ring membership. Members already present keep their keys.
func (r *Ring) Set(members []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sort.Strings(members)
	if sameStrings(members, r.members) {
		return
	}
	r.members = append([]string(nil), members...)
	r.points = r.points[:0]
	for k := range r.owners {
		delete(r.owners, k)
	}
	for _, member := range r.members {
		for i := 0; i < r.vnodes; i++ {
			point := hashPoint(member, i)
			r.points = append(r.points, point)
			r.owners[point] = member
		}
	}
	sort.Slice(r.points, func(i, j int) bool { return r.points[i] < r.points[j] })
}

// Assign returns the helper owning userID, or "" when the ring is empty.
func (r *Ring) Assign(userID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.points) == 0 {
		return ""
	}
	key := hashPoint("user:"+userID, 0)
	idx := sort.Search(len(r.points), func(i int) bool { return r.points[i] >= key })
	if idx == len(r.points) {
		idx = 0
	}
	return r.owners[r.points[idx]]
}

// Members returns a copy of the current membership.
func (r *Ring) Members() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.members...)
}

func hashPoint(seed string, vnode int) uint32 {
	h := sha256.Sum256([]byte(seed + "#" + itoa(vnode)))
	return binary.BigEndian.Uint32(h[:4])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
