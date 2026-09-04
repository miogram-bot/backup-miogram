package fleet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"miogram/internal/config"
	"miogram/internal/queue"
	"miogram/internal/telegram"
)

const (
	ModeOffPeak = "offpeak"
	ModePeak    = "peak"
)

// Manager coordinates the bot fleet: main + helper bots, peak/off-peak modes,
// user migrations and the pending delivery queue.
//
// Leadership: every instance competes for a SETNX lease (fleet:leader). The
// holder runs mode transitions, batch migrations and the pending sweeper; if
// it dies, the lease expires and another instance takes over. Non-leaders
// follow mode changes broadcast over Redis pub/sub and reconcile with the
// shared key in case a broadcast is missed.
type Manager struct {
	cfg  config.Config
	q    *queue.Queue
	ring *Ring

	mode     atomic.Value // string
	username func(botID string) string

	leaderToken atomic.Value // string; empty when not leading
	isLeader    atomic.Bool

	chatFilter   func(ctx context.Context, userID string) (bool, error)
	fallback     FallbackDeliverer
	lastBusy     atomic.Int64   // unix seconds of the last busy observation
	floodSource  func() bool    // typically (*Limiter).Flooded
	budgetSource func() float64 // typically (*Limiter).Budget

	// durableAssign mirrors routing changes into PostgreSQL during batch
	// migrations so the Redis->PG two-tier read path stays consistent.
	durableAssign func(ctx context.Context, userID, botID string) error
}

// FallbackDeliverer tries to deliver one queued message through the MAIN bot
// (Case C timeout path). It must classify Telegram responses so the caller can
// requeue or drop. Implemented in cmd wiring via tg.CallViaBot(main).
type FallbackDeliverer func(ctx context.Context, userID, method string, params map[string]any) (telegram.APIResponse, error)

func NewManager(cfg config.Config, q *queue.Queue) *Manager {
	m := &Manager{cfg: cfg, q: q, ring: NewRing(160)}
	m.mode.Store(ModeOffPeak)
	return m
}

// SetUsernameResolver lets the manager render deep-links for any bot.
func (m *Manager) SetUsernameResolver(fn func(botID string) string) {
	m.username = fn
}

// SetChatFilter wires "is this user mid-conversation" so migrations never
// break active chats. Backed by users.step LIKE 'chatting;%'.
func (m *Manager) SetChatFilter(fn func(ctx context.Context, userID string) (bool, error)) {
	m.chatFilter = fn
}

// SetFallbackDeliverer wires Case C timeout delivery through the main bot.
func (m *Manager) SetFallbackDeliverer(fn FallbackDeliverer) {
	m.fallback = fn
}

// SetFloodSource wires the live flood detector (usually (*Limiter).Flooded).
func (m *Manager) SetFloodSource(fn func() bool) {
	m.floodSource = fn
}

// SetBudgetSource exposes the adaptive limiter rate for fleet:stats keys.
func (m *Manager) SetBudgetSource(fn func() float64) {
	m.budgetSource = fn
}

// SetDurableAssign wires the PostgreSQL mirror for migration-time routing
// changes (users.assigned_bot), keeping the two-tier read path consistent.
func (m *Manager) SetDurableAssign(fn func(ctx context.Context, userID, botID string) error) {
	m.durableAssign = fn
}

func randomToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
}

func (m *Manager) Flooded() bool {
	if m.floodSource != nil {
		return m.floodSource()
	}
	return false
}

// Mode returns this instance's current view of the fleet mode.
func (m *Manager) Mode() string {
	if v, ok := m.mode.Load().(string); ok && v != "" {
		return v
	}
	return ModeOffPeak
}

// SetLocalMode overwrites this instance's view without publishing or
// persisting. Intended for admin tooling and tests; normal transitions go
// through enterMode.
func (m *Manager) SetLocalMode(mode string) {
	if mode == ModePeak || mode == ModeOffPeak {
		m.mode.Store(mode)
	}
}

// IsLeader reports whether this instance currently holds the fleet lease.
func (m *Manager) IsLeader() bool { return m.isLeader.Load() }

// Run starts heartbeats, pub/sub subscription, leadership competition,
// mode reconciliation, stats publishing and the per-instance pending sweeper.
func (m *Manager) Run(ctx context.Context) {
	if err := m.q.SetFleetMode(ctx, m.Mode()); err != nil {
		log.Printf("fleet: seed mode: %v", err)
	}
	go m.heartbeatLoop(ctx)
	go m.subscribeLoop(ctx)
	go m.followLoop(ctx)
	go m.leaderElectionLoop(ctx)
	go m.statsLoop(ctx)
	go m.pendingSweepLoop(ctx)
}

func (m *Manager) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.HeartbeatEvery)
	defer ticker.Stop()
	for {
		if err := m.q.BotHeartbeat(ctx, m.cfg.BotID, m.cfg.HeartbeatTTL); err != nil {
			log.Printf("fleet: heartbeat %s: %v", m.cfg.BotID, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) subscribeLoop(ctx context.Context) {
	sub, err := m.q.SubscribeControl(ctx)
	if err != nil {
		log.Printf("fleet: subscribe control: %v", err)
		return
	}
	defer sub.Close()
	msgs := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				return
			}
			var event struct {
				Event string `json:"event"`
				Value string `json:"value"`
			}
			if json.Unmarshal([]byte(msg.Payload), &event) != nil {
				continue
			}
			if event.Event == "mode" && (event.Value == ModePeak || event.Value == ModeOffPeak) {
				log.Printf("fleet: mode -> %s (broadcast)", event.Value)
				m.mode.Store(event.Value)
			}
		}
	}
}

// leaderElectionLoop competes for the fleet lease and runs leader duties
// (mode evaluation, migrations) only while holding it. When the lease is lost
// the duties stop and the instance retries after a short backoff.
func (m *Manager) leaderElectionLoop(ctx context.Context) {
	lease := m.cfg.LeaderLease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		token := randomToken()
		ok, err := m.q.TryLeaderLock(ctx, token, lease)
		if err != nil {
			log.Printf("fleet: leader lock: %v", err)
		}
		if ok {
			m.leaderToken.Store(token)
			m.isLeader.Store(true)
			log.Printf("fleet: instance %s acquired leadership", m.cfg.BotID)

			dutiesCtx, cancel := context.WithCancel(ctx)
			m.runLeaderDuties(dutiesCtx)
			cancel()

			// Safe no-op when the lease was taken by someone else.
			if rerr := m.q.ReleaseLeaderLock(context.Background(), token); rerr != nil {
				log.Printf("fleet: release leadership: %v", rerr)
			}
			m.isLeader.Store(false)
			m.leaderToken.Store("")
			log.Printf("fleet: instance %s released leadership", m.cfg.BotID)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(lease / 3):
		}
	}
}

// runLeaderDuties blocks while this instance leads: it renews the lease on a
// fast ticker and evaluates mode transitions; when the lease is lost or ctx is
// done it returns.
func (m *Manager) runLeaderDuties(ctx context.Context) {
	lease := m.cfg.LeaderLease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	renewTicker := time.NewTicker(lease / 3)
	evalTicker := time.NewTicker(m.cfg.ModeReconcile)
	scheduleTicker := time.NewTicker(time.Minute)
	defer renewTicker.Stop()
	defer evalTicker.Stop()
	defer scheduleTicker.Stop()

	renew := func() bool {
		token, _ := m.leaderToken.Load().(string)
		ok, err := m.q.RenewLeaderLock(ctx, token, lease)
		if err != nil || !ok {
			log.Printf("fleet: lease renewal failed; standing down")
			return false
		}
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-renewTicker.C:
			if !renew() {
				return
			}
		case <-evalTicker.C:
			if !m.evaluateMode(ctx) {
				return
			}
		case <-scheduleTicker.C:
			if !m.applySchedule(ctx) {
				return
			}
		}
	}
}

// evaluateMode decides peak/off-peak from three signals:
//  1. forced mode via FLEET_MODE env (normal/peak short-circuit everything),
//  2. busy signals: active flood limiting OR main outbound queue depth,
//  3. quiet period: no busy signal for FLEET_OFFPEAK_AFTER before downgrading.
//
// It returns false when leadership was lost and duties must stop.
func (m *Manager) evaluateMode(ctx context.Context) bool {
	switch m.cfg.FleetMode {
	case "peak":
		m.enterMode(ctx, ModePeak)
		return true
	case "normal":
		m.enterMode(ctx, ModeOffPeak)
		return true
	}

	busy := m.Flooded()
	queueLen, err := m.q.OutboundQueueLen(ctx, m.cfg.MainBotID)
	if err != nil {
		log.Printf("fleet: queue len %s: %v", m.cfg.MainBotID, err)
	} else if queueLen >= int64(m.cfg.PeakQueueLen) {
		busy = true
	}

	now := time.Now().Unix()
	if busy {
		m.lastBusy.Store(now)
		if m.Mode() != ModePeak {
			log.Printf("fleet: load pressure detected (flooded=%v queue=%d); entering peak", m.Flooded(), queueLen)
			m.enterMode(ctx, ModePeak)
		}
		return true
	}
	if m.Mode() == ModePeak && !m.scheduledPeak() {
		lastBusy := m.lastBusy.Load()
		if lastBusy == 0 || now-lastBusy >= int64(m.cfg.OffPeakAfter/time.Second) {
			log.Printf("fleet: quiet for %s; returning to off-peak", m.cfg.OffPeakAfter)
			m.enterMode(ctx, ModeOffPeak)
		}
	}
	return true
}

func (m *Manager) scheduledPeak() bool {
	return peakWindowActive(m.cfg.PeakHours, time.Now())
}

// applySchedule enforces the configured peak window (e.g. "18-24" Tehran
// hours). An empty window keeps the system purely reactive. Returns false when
// leadership was lost.
func (m *Manager) applySchedule(ctx context.Context) bool {
	if m.cfg.PeakHours == "" {
		return true
	}
	if m.scheduledPeak() {
		m.enterMode(ctx, ModePeak)
		return true
	}
	if !m.Flooded() {
		m.enterMode(ctx, ModeOffPeak)
	}
	return true
}

// followLoop keeps non-leader instances aligned with the shared mode even if
// a pub/sub broadcast is missed. The leader is authoritative and skips this.
func (m *Manager) followLoop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.ModeReconcile)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.applySharedMode(ctx)
		}
	}
}

// applySharedMode pulls the shared mode key once and applies it when this
// instance is a follower. Extracted from followLoop so tests can drive a
// single reconciliation step without real tickers.
func (m *Manager) applySharedMode(ctx context.Context) {
	if m.IsLeader() {
		return
	}
	mode := m.q.FleetMode(ctx)
	if (mode == ModePeak || mode == ModeOffPeak) && mode != m.Mode() {
		log.Printf("fleet: mode -> %s (reconcile)", mode)
		m.mode.Store(mode)
	}
}

// statsLoop publishes per-bot observability data consumed by dashboards.
func (m *Manager) statsLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.publishStats(ctx)
		}
	}
}

// publishStats writes one snapshot to fleet:stats:<bot> (60s TTL).
func (m *Manager) publishStats(ctx context.Context) {
	queueLen, _ := m.q.OutboundQueueLen(ctx, m.cfg.BotID)
	stats := map[string]any{
		"budget":    strconv.FormatFloat(m.currentBudget(), 'f', -1, 64),
		"flooded":   fmt.Sprintf("%t", m.Flooded()),
		"queue_len": strconv.FormatInt(queueLen, 10),
		"mode":      m.Mode(),
		"is_leader": fmt.Sprintf("%t", m.IsLeader()),
		"ts":        time.Now().Unix(),
	}
	key := "fleet:stats:" + m.cfg.BotID
	fields := make([]any, 0, len(stats)*2)
	for k, v := range stats {
		fields = append(fields, k, v)
	}
	pipe := m.q.Client().TxPipeline()
	pipe.HSet(ctx, key, fields...)
	pipe.Expire(ctx, key, time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("fleet: stats: %v", err)
	}
}

// currentBudget reads the wired limiter budget when available.
func (m *Manager) currentBudget() float64 {
	if m.budgetSource != nil {
		return m.budgetSource()
	}
	return -1
}

// pendingSweepLoop implements the Case C timeout path on THIS instance's bot:
// messages still queued after PendingFallback are retried through the main bot;
// undeliverable ones are requeued up to PendingAttempts then dropped with a
// sender notification.
func (m *Manager) pendingSweepLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.sweepPending(ctx, m.cfg.BotID); err != nil {
				log.Printf("fleet: pending sweep %s: %v", m.cfg.BotID, err)
			}
		}
	}
}

// sweepPending processes stale queued messages for one bot.
func (m *Manager) sweepPending(ctx context.Context, botID string) error {
	users, err := m.q.PendingUsers(ctx, botID)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	fallbackAfter := int64(m.cfg.PendingFallback / time.Second)
	for _, userID := range users {
		for {
			job, err := m.q.PopOldestPending(ctx, botID, userID)
			if err != nil {
				return err
			}
			if job == nil {
				break // list empty; index cleaned by Lua
			}
			if fallbackAfter > 0 && now-job.EnqueuedAt < fallbackAfter {
				// Not stale yet: put it back and stop touching this user.
				if err := m.q.RepushPending(ctx, botID, *job, m.cfg.PendingTTL); err != nil {
					return err
				}
				break
			}
			m.fallbackDeliver(ctx, botID, *job)
		}
	}
	return nil
}

// fallbackDeliver tries the main bot once; on failure it requeues or drops
// with a notification to the original sender.
func (m *Manager) fallbackDeliver(ctx context.Context, botID string, job queue.PendingJob) {
	if m.fallback == nil {
		// No deliverer wired (tests): keep the message queued.
		_ = m.q.RepushPending(ctx, botID, job, m.cfg.PendingTTL)
		return
	}
	var params map[string]any
	if json.Unmarshal(job.Params, &params) != nil {
		return
	}
	resp, err := m.fallback(ctx, job.UserID, job.Method, params)
	if err == nil && resp.Ok {
		log.Printf("fleet: pending fallback delivered %s to %s via %s", job.Method, job.UserID, m.cfg.MainBotID)
		return
	}
	switch {
	case err == nil && resp.PermanentlyUndeliverable():
		log.Printf("fleet: pending dropped permanently (%s) for %s", resp.Description, job.UserID)
		m.notifySender(ctx, job, "recipients account is unreachable")
	case err == nil && resp.NeedsStart() && job.Attempts+1 < m.cfg.PendingAttempts:
		job.Attempts++
		job.EnqueuedAt = 0 // rearm the fallback timer for the next cycle
		if rerr := m.q.RepushPending(ctx, botID, job, m.cfg.PendingTTL); rerr != nil {
			log.Printf("fleet: repush pending for %s: %v", job.UserID, rerr)
		}
	case err != nil:
		// Transient failure (network/timeout) talking to the main bot: requeue
		// instead of dropping, but bound retries so a permanently broken helper
		// eventually notifies the sender.
		if job.Attempts+1 >= m.cfg.PendingAttempts {
			log.Printf("fleet: pending gave up for %s after %d attempts (err=%v)", job.UserID, job.Attempts, err)
			m.notifySender(ctx, job, "delivery failed")
			return
		}
		job.Attempts++
		job.EnqueuedAt = 0
		if rerr := m.q.RepushPending(ctx, botID, job, m.cfg.PendingTTL); rerr != nil {
			log.Printf("fleet: repush pending for %s: %v", job.UserID, rerr)
		}
	default:
		log.Printf("fleet: pending gave up for %s (err=%v code=%d desc=%s)", job.UserID, err, resp.ErrorCode, resp.Description)
		m.notifySender(ctx, job, "delivery failed")
	}
}

// notifySender tells whoever sent the undeliverable message what happened.
// Sent at most once per cooldown to avoid spam.
func (m *Manager) notifySender(ctx context.Context, job queue.PendingJob, reason string) {
	if job.FromUserID == "" || job.FromUserID == job.UserID {
		return
	}
	ok, err := m.q.Client().SetNX(ctx, "fleet:sendernotice:"+job.UserID, 1, m.cfg.SuggestCooldown).Result()
	if err != nil || !ok {
		return
	}
	text := "🤖 پیام سیستم 👇\n\n" +
		"⚠️ پیام شما به کاربر مورد نظر موقتاً تحویل نشد (" + reason + ").\n" +
		"به محض بازگشت کاربر به ربات، پیام‌های در انتظار ارسال خواهد شد."
	targetBot := m.cfg.MainBotID
	if m.q != nil {
		_ = telegram.EnqueueOutbound(ctx, m.q.Client(), targetBot, "sendMessage", map[string]any{
			"chat_id": job.FromUserID,
			"text":    text,
		}, m.cfg.OutboundShardCount)
	}
}

func (m *Manager) enterMode(ctx context.Context, mode string) {
	prev := m.Mode()
	if prev == mode {
		return
	}
	if err := m.q.SetFleetMode(ctx, mode); err != nil {
		log.Printf("fleet: set mode: %v", err)
		return
	}
	m.mode.Store(mode)
	if err := m.q.PublishControl(ctx, "mode", mode); err != nil {
		log.Printf("fleet: publish mode: %v", err)
	}
	log.Printf("fleet: mode %s -> %s", prev, mode)
	switch mode {
	case ModePeak:
		// Users migrate lazily via consistent-hash suggestions on interaction;
		// nothing to do eagerly here.
	case ModeOffPeak:
		// No automatic migration on transition. Users who are on helpers
		// remain there until they initiate action by messaging the helper,
		// which shows the "return to main" link.
	}
}

// IsActive reports whether deliveries may flow through botID right now.
// The main bot is always considered active (it is the fallback target).
// Helpers are always active — in OFFPEAK they show the "return to main"
// message instead of processing messages, but they never shut down.
func (m *Manager) IsActive(botID string) bool {
	if botID == "" {
		return false
	}
	if botID == m.cfg.MainBotID {
		return true
	}
	if m.q == nil {
		// No shared state available; stay optimistic, send failures are
		// handled by the resolver fallback and pending queue.
		return true
	}
	return m.q.BotHeartbeatAlive(context.Background(), botID)
}

// AssignHelper maps a userID to a stable helper using consistent hashing.
func (m *Manager) AssignHelper(userID string) string {
	return m.ring.Assign(userID)
}

// RefreshRing syncs ring membership with configuration (call at startup).
func (m *Manager) RefreshRing() {
	helpers := make([]string, 0, len(m.cfg.Helpers))
	for id := range m.cfg.Helpers {
		helpers = append(helpers, id)
	}
	m.ring.Set(helpers)
}

// HelperUsername returns the t.me username of a bot for deep-links.
func (m *Manager) HelperUsername(botID string) string {
	if m.username != nil {
		if u := m.username(botID); u != "" {
			return u
		}
	}
	if botID == m.cfg.MainBotID {
		return "@" + m.cfg.MainBotUsername
	}
	return m.cfg.Helpers[botID]
}

// DefaultUsername is the conventional username mapping for legacy shard IDs.
func DefaultUsername(botID string) string {
	return legacyBotUsernames[botID]
}

var legacyBotUsernames = map[string]string{
	"main":   "miogram_bot",
	"shard1": "miogram_shard1_bot",
	"shard2": "miogram_shard2_bot",
	"shard3": "miogram_shard3_bot",
	"shard4": "miogram_shard4_bot",
	"shard5": "miogram_shard5_bot",
}

// SuggestMigration sends the peak-hour handoff message with a deep-link button
// to the helper chosen by consistent hashing. The link carries the user ID
// (?start=classroom_<id>) so the helper registers the user and flushes pending
// deliveries. Caller enforces cooldowns.
func (m *Manager) SuggestMigration(ctx context.Context, send func(method string, params map[string]any) error, userID string) (string, error) {
	helper := m.AssignHelper(userID)
	username := m.HelperUsername(helper)
	if username == "" {
		return "", nil
	}
	link := "https://t.me/" + strings.TrimPrefix(username, "@") + "?start=classroom_" + userID
	err := send("sendMessage", map[string]any{
		"chat_id": userID,
		"text": "🤖 پیام سیستم 👇\n\n" +
			"⚠️ ربات اصلی در ساعات پرترافیک قرار دارد. برای ادامه بدون وقفه و دریافت پیام‌ها از ربات کمکی استفاده کنید 👇",
		"reply_markup": json.RawMessage(`{"inline_keyboard":[[{"text":"🚀 انتقال به ربات کمکی","url":"` + link + `"}]]}`),
	})
	return helper, err
}

// EnqueuePending stores an undeliverable message for a user who has never
// started the destination bot (Case B). fromUserID enables sender
// notification when the Case C timeout fallback gives up.
func (m *Manager) EnqueuePending(ctx context.Context, botID, userID, fromUserID, method string, params map[string]any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return m.q.PushPending(ctx, botID, userID, queue.PendingJob{
		UserID:     userID,
		FromUserID: fromUserID,
		Method:     method,
		Params:     raw,
	}, m.cfg.PendingTTL)
}

// DrainPending flushes queued messages after the user started this bot.
func (m *Manager) DrainPending(ctx context.Context, botID, userID string, deliver func(method string, params map[string]any)) {
	jobs, err := m.q.TakePending(ctx, botID, userID)
	if err != nil {
		log.Printf("fleet: take pending %s/%s: %v", botID, userID, err)
		return
	}
	for _, job := range jobs {
		var params map[string]any
		if json.Unmarshal(job.Params, &params) != nil {
			continue
		}
		deliver(job.Method, params)
	}
	if len(jobs) > 0 {
		log.Printf("fleet: flushed %d pending messages for %s on %s", len(jobs), userID, botID)
	}
}

// HasPending reports whether any user owes a start-flush on this bot.
func (m *Manager) PendingUsers(ctx context.Context, botID string) ([]string, error) {
	return m.q.PendingUsers(ctx, botID)
}

// migrateHelpersToMain batch-updates every routing entry pointing at a helper
// back to the main bot and notifies affected users once (off-peak MAU push).
//
// Active-chat protection: users whose step is 'chatting;...' are skipped and
// stay on their helper until the conversation ends; the per-update off-peak
// interception moves them right after. This implements "never force-move a
// user who is in an active chat".
func (m *Manager) migrateHelpersToMain(ctx context.Context) {
	if len(m.cfg.Helpers) == 0 {
		return
	}
	for helper := range m.cfg.Helpers {
		users, err := m.q.UsersMappedTo(ctx, helper)
		if err != nil {
			log.Printf("fleet: users mapped to %s: %v", helper, err)
			continue
		}
		moved, deferred := 0, 0
		for _, userID := range users {
			if chatting, cerr := m.isUserChatting(ctx, userID); cerr != nil {
				// On a chat-check error, skip the user rather than risk moving a
				// chatting one. The next reconcile will retry.
				log.Printf("fleet: chat check %s: %v (skipping)", userID, cerr)
				deferred++
				continue
			} else if chatting {
				deferred++
				continue
			}
			changed, err := m.q.MoveUserBot(ctx, userID, m.cfg.MainBotID)
			if err != nil {
				log.Printf("fleet: move %s -> %s: %v", userID, m.cfg.MainBotID, err)
				continue
			}
			if changed {
				moved++
				if m.durableAssign != nil {
					if err := m.durableAssign(ctx, userID, m.cfg.MainBotID); err != nil {
						log.Printf("fleet: durable assign %s -> %s: %v", userID, m.cfg.MainBotID, err)
					}
				}
			}
			if changed && m.cfg.MigrationNotify {
				m.notifyReturnToMain(ctx, userID, helper)
			}
		}
		if moved > 0 || deferred > 0 {
			log.Printf("fleet: offpeak migrated %d users from %s to %s (%d deferred: active chat)",
				moved, helper, m.cfg.MainBotID, deferred)
		}
		// Renew the leader lease after each helper so a long migration can't
		// lose leadership mid-run.
		if token, ok := m.leaderToken.Load().(string); ok && token != "" {
			if _, rerr := m.q.RenewLeaderLock(context.Background(), token, m.cfg.LeaderLease); rerr != nil {
				log.Printf("fleet: renew leader lock during migration: %v", rerr)
			}
		}
	}
}

// isUserChatting consults the wired filter; unknown users count as free.
func (m *Manager) isUserChatting(ctx context.Context, userID string) (bool, error) {
	if m.chatFilter == nil {
		return false, nil
	}
	return m.chatFilter(ctx, userID)
}

func (m *Manager) notifyReturnToMain(ctx context.Context, userID, helper string) {
	username := strings.TrimPrefix(m.HelperUsername(m.cfg.MainBotID), "@")
	text := "🤖 پیام سیستم 👇\n\n" +
		"✅ ساعات پرترافیک به پایان رسید و ربات کمکی موقتاً غیرفعال شد.\n" +
		"برای ادامه گفتگو و استفاده از ربات به ربات اصلی برگردید 👇\n" +
		"https://t.me/" + username
	// Notifications must arrive from the MAIN bot so users reconnect there.
	if err := telegram.EnqueueOutbound(ctx, m.q.Client(), m.cfg.MainBotID, "sendMessage", map[string]any{
		"chat_id":                  userID,
		"text":                     text,
		"disable_web_page_preview": true,
	}, m.cfg.OutboundShardCount); err != nil {
		log.Printf("fleet: notify return-to-main %s: %v", userID, err)
	}
}

func peakWindowActive(spec string, now time.Time) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return false
	}
	fields := strings.SplitN(spec, "-", 2)
	if len(fields) != 2 {
		return false
	}
	from, err1 := strconv.Atoi(strings.TrimSpace(fields[0]))
	to, err2 := strconv.Atoi(strings.TrimSpace(fields[1]))
	if err1 != nil || err2 != nil {
		return false
	}
	hour := now.Hour()
	if from <= to {
		return hour >= from && hour < to
	}
	// Window wraps midnight, e.g. 21-02.
	return hour >= from || hour < to
}
