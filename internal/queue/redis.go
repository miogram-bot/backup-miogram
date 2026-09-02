package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Queue struct {
	client *redis.Client
	key    string
}

func New(addr, password string, db int, key string) *Queue {
	return &Queue{
		client: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
			PoolSize: 100,
		}),
		key: key,
	}
}

// NewWithClient wraps an existing Redis connection (tests, shared instances).
func NewWithClient(client *redis.Client) *Queue {
	return &Queue{client: client}
}

func (q *Queue) Client() *redis.Client {
	return q.client
}

func (q *Queue) Ping(ctx context.Context) error {
	return q.client.Ping(ctx).Err()
}

// ---------- توابع صف‌های ورودی/خروجی بر اساس botID ----------

func (q *Queue) EnqueueInbound(ctx context.Context, botID string, payload []byte) error {
	return q.client.RPush(ctx, "inbound:"+botID, payload).Err()
}

func (q *Queue) EnqueueInboundUnique(ctx context.Context, botID string, updateID int64, payload []byte, ttl time.Duration) (bool, error) {
	key := "miogram:update:" + botID + ":" + int64String(updateID)
	result, err := q.client.Eval(ctx, enqueueUniqueScript, []string{key, "inbound:" + botID}, "1", ttl.Milliseconds(), payload).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (q *Queue) DequeueInbound(ctx context.Context, botID string, timeout time.Duration) ([]byte, error) {
	result, err := q.client.BLPop(ctx, timeout, "inbound:"+botID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(result) != 2 {
		return nil, nil
	}
	return []byte(result[1]), nil
}

func (q *Queue) EnqueueOutbound(ctx context.Context, botID string, payload []byte) error {
	return q.client.RPush(ctx, "outbound:"+botID, payload).Err()
}

func (q *Queue) DequeueOutbound(ctx context.Context, botID string, shardIndex int, timeout time.Duration) ([]byte, error) {
	key := fmt.Sprintf("outbound:%s:shard:%d", botID, shardIndex)
	result, err := q.client.BLPop(ctx, timeout, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(result) != 2 {
		return nil, nil
	}
	return []byte(result[1]), nil
}

// ---------- توابع نگاشت کاربر به ربات (user_bot_routing) ----------

func (q *Queue) SetUserBot(ctx context.Context, userID, botID string) error {
	shard := userShard(userID)
	key := fmt.Sprintf("user_bot_routing:%d", shard)
	return q.client.HSet(ctx, key, userID, botID).Err()
}

// RegisterUserBot implements the Registrar write rule: skip the write when the
// mapping already points at botID. It atomically maintains the reverse index
// user_bot_reverse:<botID> used for off-peak batch migrations. Returns true if
// the mapping changed.
func (q *Queue) RegisterUserBot(ctx context.Context, userID, botID string) (bool, error) {
	shard := userShard(userID)
	res, err := q.client.Eval(ctx, registerUserBotScript, []string{
		fmt.Sprintf("user_bot_routing:%d", shard),
		"user_bot_reverse:" + botID,
	}, userID, botID).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

// MoveUserBot force-moves a user to botID (migration path); returns true when
// the mapping changed.
func (q *Queue) MoveUserBot(ctx context.Context, userID, botID string) (bool, error) {
	return q.RegisterUserBot(ctx, userID, botID)
}

func (q *Queue) GetUserBot(ctx context.Context, userID string) (string, error) {
	shard := userShard(userID)
	key := fmt.Sprintf("user_bot_routing:%d", shard)
	botID, err := q.client.HGet(ctx, key, userID).Result()
	if err == redis.Nil {
		return "", nil
	}
	return botID, err
}

// UsersMappedTo returns every userID currently routed to botID via the reverse index.
func (q *Queue) UsersMappedTo(ctx context.Context, botID string) ([]string, error) {
	return q.client.SMembers(ctx, "user_bot_reverse:"+botID).Result()
}

// ---------- fleet mode, heartbeats and control channel ----------

const (
	modeKey      = "fleet:mode"
	controlChan  = "fleet:control"
	heartbeatPfx = "fleet:hb:"
)

func (q *Queue) SetFleetMode(ctx context.Context, mode string) error {
	return q.client.Set(ctx, modeKey, mode, 0).Err()
}

func (q *Queue) FleetMode(ctx context.Context) string {
	mode, err := q.client.Get(ctx, modeKey).Result()
	if err != nil {
		return ""
	}
	return mode
}

// PublishControl broadcasts a fleet event to all bot instances.
func (q *Queue) PublishControl(ctx context.Context, event, value string) error {
	payload, _ := json.Marshal(map[string]string{"event": event, "value": value})
	return q.client.Publish(ctx, controlChan, payload).Err()
}

// SubscribeControl subscribes this instance to fleet-wide events.
func (q *Queue) SubscribeControl(ctx context.Context) (*redis.PubSub, error) {
	sub := q.client.Subscribe(ctx, controlChan)
	if _, err := sub.Receive(ctx); err != nil {
		sub.Close()
		return nil, err
	}
	return sub, nil
}

// BotHeartbeat refreshes this bot's liveness key; it expires after ttl so a
// crashed helper is treated as inactive by the Resolver.
func (q *Queue) BotHeartbeat(ctx context.Context, botID string, ttl time.Duration) error {
	return q.client.Set(ctx, heartbeatPfx+botID, time.Now().UnixMilli(), ttl).Err()
}

func (q *Queue) BotHeartbeatAlive(ctx context.Context, botID string) bool {
	// EXISTS returns a count; Err() alone would be nil even for a missing
	// key, so the count decides liveness.
	n, err := q.client.Exists(ctx, heartbeatPfx+botID).Result()
	return err == nil && n > 0
}

// ---------- pending delivery queue (Case B / Case C fallback) ----------

type PendingJob struct {
	UserID     string          `json:"user_id"`
	FromUserID string          `json:"from_user_id,omitempty"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
	EnqueuedAt int64           `json:"enqueued_at,omitempty"` // unix seconds, rearmed on requeue
	Attempts   int             `json:"attempts,omitempty"`    // fallback delivery attempts
}

// PushPending stores an undeliverable outbound message for later flush.
func (q *Queue) PushPending(ctx context.Context, botID, userID string, job PendingJob, ttl time.Duration) error {
	if job.EnqueuedAt == 0 {
		job.EnqueuedAt = time.Now().Unix()
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	list := fmt.Sprintf("pending:%s:%s", botID, userID)
	index := fmt.Sprintf("pending:index:%s", botID)
	pipe := q.client.TxPipeline()
	pipe.RPush(ctx, list, raw)
	pipe.Expire(ctx, list, ttl)
	pipe.SAdd(ctx, index, userID)
	pipe.Expire(ctx, index, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// TakePending atomically drains and deletes all pending jobs of one user.
func (q *Queue) TakePending(ctx context.Context, botID, userID string) ([]PendingJob, error) {
	list := fmt.Sprintf("pending:%s:%s", botID, userID)
	raw, err := q.client.Eval(ctx, takePendingScript, []string{list,
		fmt.Sprintf("pending:index:%s", botID)}, userID).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	// go-redis decodes Lua bulk arrays as []interface{} of strings.
	items, ok := raw.([]interface{})
	if !ok {
		return nil, nil
	}
	jobs := make([]PendingJob, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		var job PendingJob
		if json.Unmarshal([]byte(s), &job) == nil && job.Method != "" {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// PopOldestPending atomically removes and returns the oldest pending job of a
// user, cleaning the index when the list becomes empty. Used by the fallback
// sweeper which must decide per message instead of draining blindly.
func (q *Queue) PopOldestPending(ctx context.Context, botID, userID string) (*PendingJob, error) {
	list := fmt.Sprintf("pending:%s:%s", botID, userID)
	raw, err := q.client.Eval(ctx, popOldestPendingScript, []string{list,
		fmt.Sprintf("pending:index:%s", botID)}, userID).Text()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	var job PendingJob
	if json.Unmarshal([]byte(raw), &job) != nil || job.Method == "" {
		return nil, nil
	}
	return &job, nil
}

// RepushPending returns an undeliverable job to the tail of the pending list
// (fallback retry). The caller owns EnqueuedAt: pass zero for a fresh
// timestamp, or keep the original value when preserving age matters.
func (q *Queue) RepushPending(ctx context.Context, botID string, job PendingJob, ttl time.Duration) error {
	if job.EnqueuedAt == 0 {
		job.EnqueuedAt = time.Now().Unix()
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	list := fmt.Sprintf("pending:%s:%s", botID, job.UserID)
	index := fmt.Sprintf("pending:index:%s", botID)
	pipe := q.client.TxPipeline()
	pipe.RPush(ctx, list, raw)
	pipe.Expire(ctx, list, ttl)
	pipe.SAdd(ctx, index, job.UserID)
	_, err = pipe.Exec(ctx)
	return err
}

func (q *Queue) PendingUsers(ctx context.Context, botID string) ([]string, error) {
	return q.client.SMembers(ctx, fmt.Sprintf("pending:index:%s", botID)).Result()
}

// ---------- leader election (SETNX lease) ----------

const leaderKey = "fleet:leader"

// TryLeaderLock acquires the fleet leadership lease if free. The token makes
// release/renew safe for the holder only.
func (q *Queue) TryLeaderLock(ctx context.Context, token string, lease time.Duration) (bool, error) {
	return q.client.SetNX(ctx, leaderKey, token, lease).Result()
}

// RenewLeaderLock extends the lease only if this instance still holds it.
func (q *Queue) RenewLeaderLock(ctx context.Context, token string, lease time.Duration) (bool, error) {
	res, err := q.client.Eval(ctx, renewLeaderScript, []string{leaderKey}, token, int64(lease/time.Millisecond)).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

// ReleaseLeaderLock frees the leadership lease if this instance holds it.
func (q *Queue) ReleaseLeaderLock(ctx context.Context, token string) error {
	return q.client.Eval(ctx, releaseLeaderScript, []string{leaderKey}, token).Err()
}

// OutboundQueueLen reports the total depth of a bot's outbound queues
// (legacy key + all sharded keys).
func (q *Queue) OutboundQueueLen(ctx context.Context, botID string) (int64, error) {
	var total int64
	n, err := q.client.LLen(ctx, "outbound:"+botID).Result()
	if err != nil {
		return 0, err
	}
	total += n
	prefix := "outbound:" + botID + ":shard:"
	var cursor uint64
	for {
		keys, nextCursor, scanErr := q.client.Scan(ctx, cursor, prefix+"*", 100).Result()
		if scanErr != nil {
			break
		}
		for _, k := range keys {
			ln, lerr := q.client.LLen(ctx, k).Result()
			if lerr != nil {
				continue
			}
			total += ln
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return total, nil
}

// ---------- توابع بار ربات‌ها ----------

func (q *Queue) UpdateBotLoad(ctx context.Context, botID string, score int) error {
	return q.client.ZAdd(ctx, "bot:loads", redis.Z{Score: float64(score), Member: botID}).Err()
}

// ---------- توابع قفل و unique عمومی ----------

func (q *Queue) Enqueue(ctx context.Context, payload []byte) error {
	return q.client.RPush(ctx, q.key, payload).Err()
}

func (q *Queue) EnqueueUnique(ctx context.Context, updateID int64, payload []byte, ttl time.Duration) (bool, error) {
	key := "miogram:update:" + int64String(updateID)
	result, err := q.client.Eval(ctx, enqueueUniqueScript, []string{key, q.key}, "1", ttl.Milliseconds(), payload).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (q *Queue) Dequeue(ctx context.Context, timeout time.Duration) ([]byte, error) {
	result, err := q.client.BLPop(ctx, timeout, q.key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(result) != 2 {
		return nil, nil
	}
	return []byte(result[1]), nil
}

func (q *Queue) AcquireLock(ctx context.Context, key string, ttl, wait time.Duration) (func(context.Context) error, error) {
	token := randomToken()
	deadline := time.Now().Add(wait)
	for {
		ok, err := q.client.SetNX(ctx, key, token, ttl).Result()
		if err != nil {
			return nil, err
		}
		if ok {
			return func(releaseCtx context.Context) error {
				return q.client.Eval(releaseCtx, releaseScript, []string{key}, token).Err()
			}, nil
		}
		if wait <= 0 || time.Now().After(deadline) {
			return nil, errors.New("lock wait timeout")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ---------- اسکریپت‌های Lua ----------

const releaseScript = `
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0
`

// registerUserBotScript: compare-then-skip routing write (Registrar rule).
const registerUserBotScript = `
-- KEYS[1] = user_bot_routing:<shard>
-- KEYS[2] = user_bot_reverse:<newBot>
-- ARGV[1] = userID, ARGV[2] = newBotID
local old = redis.call('HGET', KEYS[1], ARGV[1])
if old == ARGV[2] then
	return 0
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('SADD', KEYS[2], ARGV[1])
if old and old ~= '' then
	redis.call('SREM', 'user_bot_reverse:' .. old, ARGV[1])
end
return 1
`

// takePendingScript: atomically drain a user's pending jobs and clean the index.
const takePendingScript = `
-- KEYS[1] = pending:<bot>:<user>
-- KEYS[2] = pending:index:<bot>
-- ARGV[1] = userID
local items = redis.call('LRANGE', KEYS[1], 0, -1)
redis.call('DEL', KEYS[1])
redis.call('SREM', KEYS[2], ARGV[1])
return items
`

// popOldestPendingScript: pop exactly one job, cleaning the index when empty.
const popOldestPendingScript = `
-- KEYS[1] = pending:<bot>:<user>
-- KEYS[2] = pending:index:<bot>
-- ARGV[1] = userID
local item = redis.call('LPOP', KEYS[1])
if not item then
	redis.call('SREM', KEYS[2], ARGV[1])
	return false
end
if redis.call('LLEN', KEYS[1]) == 0 then
	redis.call('SREM', KEYS[2], ARGV[1])
end
return item
`

// renewLeaderScript: extend the lease only for the current holder (check-and-expire).
const renewLeaderScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

const releaseLeaderScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`

const enqueueUniqueScript = `
if redis.call("set", KEYS[1], ARGV[1], "PX", ARGV[2], "NX") then
	redis.call("rpush", KEYS[2], ARGV[3])
	return 1
end
return 0
`

func randomToken() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return time.Now().Format(time.RFC3339Nano)
	}
	return hex.EncodeToString(buf[:])
}

func int64String(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func userShard(userID string) int {
	// هش ساده با جمع کد کاراکترها
	sum := 0
	for _, r := range userID {
		sum += int(r)
	}
	return sum % 10000
}
