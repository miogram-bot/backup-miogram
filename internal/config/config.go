package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Addr                 string
	PublicBaseURL        string
	IsDevelop            bool
	BotToken             string
	BotName              string
	BotUsername          string
	MainBotUsername      string // username of the main bot, known to every instance (for "move to main" links)
	AdminID              string
	MerchantID           string
	PaymentGateway       string
	DatabaseURL          string
	RedisAddr            string
	RedisPassword        string
	RedisDB              int
	UpdateQueueKey       string
	WorkerCount          int
	VerifyTelegramIP     bool
	WebhookSecret        string
	CertPath             string
	WebhookPath          string
	SetWebhookOnStart    bool
	CloudflaredPath      string
	DevelopTunnelTimeout time.Duration
	FilesDir             string
	LegacyRunPath        string
	HTTPTimeout          time.Duration
	JobInterval          time.Duration
	DBMaxConns           int32
	PerUserLockTTL       time.Duration
	PerUserLockWait      time.Duration
	TelegramSOCKS5Proxy  string
	BotID                string

	// Bot fleet (main + helpers) and adaptive rate limiting.
	MainBotID       string
	Helpers         map[string]string // botID -> @username
	FleetMode       string            // auto | normal | peak (forced overrides)
	PeakHours       string            // e.g. "18-24" Tehran time; empty = reactive only
	PeakQueueLen    int               // outbound queue depth that signals peak
	OffPeakAfter    time.Duration     // sustained quiet period before downgrading
	MigrationNotify bool
	AIMDSeed        float64 // initial send budget (msg/s)
	AIMDFloor       float64 // lowest budget after decreases
	AIMDIncrement   float64 // additive increase per second without floods
	AIMDDecay       float64 // multiplicative decrease factor on flood wait
	AIMDMax         float64 // optional hard cap; 0 = feedback-driven only
	FloodCooldown   time.Duration
	PendingTTL      time.Duration
	PendingFallback time.Duration // age after which queued messages fall back to the main bot
	PendingAttempts int           // fallback delivery attempts before dropping + notifying sender
	LeaderLease     time.Duration
	HeartbeatTTL    time.Duration
	HeartbeatEvery  time.Duration
	ModeReconcile   time.Duration
	SuggestCooldown time.Duration
}

func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		Addr:                 env("APP_ADDR", ":8080"),
		PublicBaseURL:        trimRightSlash(env("PUBLIC_BASE_URL", "")),
		IsDevelop:            envBool("IS_DEVELOP", false),
		BotToken:             env("BOT_TOKEN", ""),
		BotName:              env("BOT_NAME", "اسم ربات"),
		BotUsername:          strings.TrimPrefix(env("BOT_USERNAME", "full_chat_bot"), "@"),
		AdminID:              env("ADMIN_ID", "71983499"),
		MerchantID:           env("MERCHANT_ID", ""),
		PaymentGateway:       env("PAYMENT_GATEWAY", "zarinpal"),
		DatabaseURL:          env("DATABASE_URL", "postgres://miogram:miogram@localhost:5432/miogram?sslmode=disable"),
		RedisAddr:            env("REDIS_ADDR", "localhost:6379"),
		RedisPassword:        env("REDIS_PASSWORD", ""),
		RedisDB:              envInt("REDIS_DB", 0),
		UpdateQueueKey:       env("UPDATE_QUEUE_KEY", "miogram:telegram:updates"),
		WorkerCount:          envInt("WORKER_COUNT", 16),
		VerifyTelegramIP:     envBool("VERIFY_TELEGRAM_IP", true),
		WebhookSecret:        env("WEBHOOK_SECRET", ""),
		CertPath:             env("CERT_PATH", "/app/ssl/cert.pem"),
		WebhookPath:          env("WEBHOOK_PATH", "/bot/user/index.php"),
		SetWebhookOnStart:    envBool("SET_WEBHOOK_ON_START", false),
		CloudflaredPath:      env("CLOUDFLARED_PATH", "cloudflared"),
		DevelopTunnelTimeout: envDuration("DEVELOP_TUNNEL_TIMEOUT", 45*time.Second),
		FilesDir:             env("FILES_DIR", "bot/files"),
		LegacyRunPath:        env("LEGACY_RUN_PATH", ""),
		HTTPTimeout:          envDuration("HTTP_TIMEOUT", 10*time.Second),
		JobInterval:          envDuration("JOB_INTERVAL", 60*time.Second),
		DBMaxConns:           int32(envInt("DB_MAX_CONNS", 40)),
		PerUserLockTTL:       envDuration("PER_USER_LOCK_TTL", 30*time.Second),
		PerUserLockWait:      envDuration("PER_USER_LOCK_WAIT", 15*time.Second),
		TelegramSOCKS5Proxy:  env("TELEGRAM_SOCKS5_PROXY", ""),
		BotID:                env("BOT_ID", "main"),
	}
	cfg.PaymentGateway = normalizeGateway(cfg.PaymentGateway)
	cfg.WebhookPath = ensureSlash(cfg.WebhookPath)
	if cfg.WorkerCount < 1 {
		cfg.WorkerCount = 1
	}
	if cfg.BotToken == "" {
		return cfg, errors.New("BOT_TOKEN is required")
	}
	cfg.MainBotID = env("FLEET_MAIN_BOT", "main")
	cfg.MainBotUsername = strings.TrimPrefix(env("MAIN_BOT_USERNAME", ""), "@")
	if cfg.MainBotUsername == "" {
		cfg.MainBotUsername = cfg.BotUsername
	}
	helpers := parseHelpers(env("FLEET_HELPERS", ""))
	delete(helpers, cfg.MainBotID)
	cfg.Helpers = helpers
	cfg.FleetMode = strings.ToLower(env("FLEET_MODE", "auto"))
	if cfg.FleetMode != "auto" && cfg.FleetMode != "normal" && cfg.FleetMode != "peak" {
		cfg.FleetMode = "auto"
	}
	cfg.PeakHours = strings.TrimSpace(env("FLEET_PEAK_HOURS", ""))
	cfg.PeakQueueLen = envInt("FLEET_PEAK_QUEUE_LEN", 200)
	cfg.OffPeakAfter = envDuration("FLEET_OFFPEAK_AFTER", 10*time.Minute)
	cfg.MigrationNotify = envBool("FLEET_MIGRATION_NOTIFY", true)
	cfg.AIMDSeed = envFloat("AIMD_SEED", 20)
	cfg.AIMDFloor = envFloat("AIMD_FLOOR", 2)
	cfg.AIMDIncrement = envFloat("AIMD_INCREMENT", 1)
	cfg.AIMDDecay = envFloat("AIMD_DECAY", 0.5)
	cfg.AIMDMax = envFloat("AIMD_MAX", 0)
	if cfg.AIMDDecay <= 0 || cfg.AIMDDecay >= 1 {
		cfg.AIMDDecay = 0.5
	}
	if cfg.AIMDFloor < 1 {
		cfg.AIMDFloor = 1
	}
	if cfg.AIMDSeed < cfg.AIMDFloor {
		cfg.AIMDSeed = cfg.AIMDFloor
	}
	cfg.FloodCooldown = envDuration("FLOOD_COOLDOWN", 45*time.Second)
	cfg.PendingTTL = envDuration("PENDING_TTL", 48*time.Hour)
	cfg.PendingFallback = envDuration("PENDING_FALLBACK", 15*time.Minute)
	cfg.PendingAttempts = envInt("PENDING_MAX_ATTEMPTS", 4)
	cfg.LeaderLease = envDuration("LEADER_LEASE", 30*time.Second)
	cfg.HeartbeatTTL = envDuration("HEARTBEAT_TTL", 45*time.Second)
	cfg.HeartbeatEvery = envDuration("HEARTBEAT_EVERY", 15*time.Second)
	cfg.ModeReconcile = envDuration("MODE_RECONCILE", 10*time.Second)
	cfg.SuggestCooldown = envDuration("SUGGEST_COOLDOWN", 6*time.Hour)
	return cfg, nil
}

// IsHelper reports whether this process runs a helper (non-main) bot.
func (c Config) IsHelper() bool {
	return c.BotID != c.MainBotID
}

func parseHelpers(raw string) map[string]string {
	helpers := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		id := strings.TrimSpace(parts[0])
		if id == "" {
			continue
		}
		username := ""
		if len(parts) == 2 {
			username = strings.TrimSpace(parts[1])
		}
		helpers[id] = username
	}
	return helpers
}

func envFloat(key string, fallback float64) float64 {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || f < 0 {
		return fallback
	}
	return f
}

func (c Config) WebhookURL() string {
	if c.PublicBaseURL == "" {
		return ""
	}
	return trimRightSlash(c.PublicBaseURL) + c.WebhookPath
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(env(key, ""))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := env(key, "")
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err == nil {
		return d
	}
	seconds, err := strconv.Atoi(value)
	if err == nil {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func ensureSlash(s string) string {
	if s == "" {
		return "/"
	}
	if strings.HasPrefix(s, "/") {
		return s
	}
	return "/" + s
}

func trimRightSlash(s string) string {
	return strings.TrimRight(s, "/")
}

func normalizeGateway(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "zarinpanl", "zarinpal", "":
		return "zarinpal"
	case "idpay":
		return "idpay"
	default:
		return s
	}
}
