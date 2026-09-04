package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"miogram/internal/bot"
	"miogram/internal/config"
	"miogram/internal/fleet"
	"miogram/internal/jobs"
	"miogram/internal/payments"
	"miogram/internal/queue"
	"miogram/internal/storage"
	"miogram/internal/telegram"
	"miogram/internal/tunnel"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	// Production must have a webhook secret; never fail open (H1).
	if !cfg.IsDevelop && cfg.WebhookSecret == "" {
		log.Fatalf("FATAL: WEBHOOK_SECRET is required in production mode. Set WEBHOOK_SECRET or IS_DEVELOP=true")
	}
	q := queue.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, cfg.UpdateQueueKey)
	if err := q.Ping(ctx); err != nil {
		log.Fatalf("redis: %v", err)
	}
	store, err := storage.New(ctx, cfg.DatabaseURL, cfg.DBMaxConns, q.Client())
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, cfg.LegacyRunPath); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	// تغییر مهم: ارسال cfg.BotID به NewClient
	limiter := fleet.NewLimiter(fleet.LimiterConfig{
		Seed:      cfg.AIMDSeed,
		Floor:     cfg.AIMDFloor,
		Increment: cfg.AIMDIncrement,
		Decay:     cfg.AIMDDecay,
		Max:       cfg.AIMDMax,
		Cooldown:  cfg.FloodCooldown,
	})
	tg, err := telegram.NewClient(cfg.BotToken, cfg.HTTPTimeout, cfg.TelegramSOCKS5Proxy, cfg.BotID, limiter)
	if err != nil {
		log.Fatalf("telegram client: %v", err)
	}
	tg.StartOutboundQueue(ctx, q.Client())

	fleetManager := fleet.NewManager(cfg, q)
	fleetManager.RefreshRing()
	fleetManager.SetFloodSource(limiter.Flooded)
	fleetManager.SetBudgetSource(limiter.Budget)
		fleetManager.SetUsernameResolver(func(botID string) string {
			if botID == cfg.MainBotID {
				return "@" + cfg.MainBotUsername
			}
			if u, ok := cfg.Helpers[botID]; ok && u != "" {
				return u
			}
			return fleet.DefaultUsername(botID)
		})
	// Active-chat protection for migrations and off-peak bounces.
	fleetManager.SetChatFilter(func(ctx context.Context, userID string) (bool, error) {
		var step string
		err := store.DB().QueryRow(ctx, `SELECT step FROM users WHERE user_id=$1`, userID).Scan(&step)
		if err != nil {
			return false, err
		}
		return strings.HasPrefix(step, "chatting;"), nil
	})
	// Case C timeout fallback: retry queued messages through the main bot so
	// responses can be classified (NeedsStart / PermanentlyUndeliverable).
	fleetManager.SetFallbackDeliverer(func(ctx context.Context, userID, method string, params map[string]any) (telegram.APIResponse, error) {
		return tg.CallViaBot(ctx, cfg.MainBotID, method, params)
	})
	// Mirror batch-migration routing changes into PostgreSQL (assigned_bot).
	fleetManager.SetDurableAssign(store.UpdateAssignedBot)
	go fleetManager.Run(ctx)

	var devTunnel *tunnel.QuickTunnel
	if cfg.IsDevelop {
		originURL := tunnel.OriginURL(cfg.Addr)
		devTunnel, err = tunnel.StartQuick(ctx, cfg.CloudflaredPath, originURL, cfg.DevelopTunnelTimeout)
		if err != nil {
			log.Fatalf("development tunnel: %v", err)
		}
		defer func() { _ = devTunnel.Close() }()
		cfg.PublicBaseURL = devTunnel.URL()
		cfg.SetWebhookOnStart = true
		cfg.VerifyTelegramIP = false
		log.Printf("development tunnel ready: %s -> %s", cfg.PublicBaseURL, originURL)
	}

	// اصلاح‌شده: ارسال q به عنوان آرگومان چهارم
	botService := bot.New(cfg, store, tg, q, fleetManager)

	// افزودن مانیتورینگ بار
	go botService.StartLoadMonitoring(ctx, q)

	if err := botService.WarmHelpAssets(ctx); err != nil {
		log.Printf("warm help assets: %v", err)
	}
	payService := payments.New(cfg, store, tg)
	botService.SetPayments(payService)
	scheduler := jobs.New(cfg, store, tg, q, botService)

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			worker(ctx, id, cfg, q, botService)
		}(i + 1)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		scheduler.Run(ctx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc(cfg.WebhookPath, webhookHandler(cfg, q))
	mux.HandleFunc("/webhook", webhookHandler(cfg, q))
	mux.HandleFunc("/pay/request.php", payService.Request)
	mux.HandleFunc("/pay/verify.php", payService.Verify)
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(cfg.FilesDir))))

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		if devTunnel != nil {
			_ = devTunnel.Close()
		}
		log.Fatalf("http listen: %v", err)
	}
	go func() {
		log.Printf("miogram (%s) listening on %s with %d workers", cfg.BotID, cfg.Addr, cfg.WorkerCount)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http: %v", err)
			stop()
		}
	}()

	if cfg.SetWebhookOnStart {
		if url := cfg.WebhookURL(); url != "" {
			if err := tg.SetWebhook(ctx, url, cfg.WebhookSecret, cfg.CertPath); err != nil {
				log.Printf("set webhook failed: %v", err)
			} else {
				log.Printf("telegram webhook set to %s", url)
			}
		}
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	wg.Wait()
}

func webhookHandler(cfg config.Config, q *queue.Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Require a configured secret in production; never fail open (H1).
		if !cfg.IsDevelop && cfg.WebhookSecret == "" {
			log.Printf("webhook: WEBHOOK_SECRET not configured in production")
			http.Error(w, "server misconfigured", http.StatusInternalServerError)
			return
		}
		if cfg.WebhookSecret != "" && r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != cfg.WebhookSecret {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if cfg.VerifyTelegramIP && !isTelegramIP(clientIP(r)) && !isPrivateOrLoopback(clientIP(r)) {
			http.Error(w, "Not allowed", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var up telegram.Update
		if err := json.Unmarshal(body, &up); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		botID := cfg.BotID
		if botID == "" {
			http.Error(w, "bot id not configured", http.StatusInternalServerError)
			return
		}

		if up.UpdateID != 0 {
			ok, err := q.EnqueueInboundUnique(r.Context(), botID, up.UpdateID, body, 24*time.Hour)
			if err != nil {
				http.Error(w, "queue error", http.StatusInternalServerError)
				return
			}
			if !ok {
				_, _ = io.WriteString(w, "OK")
				return
			}
		} else {
			if err := q.EnqueueInbound(r.Context(), botID, body); err != nil {
				http.Error(w, "queue error", http.StatusInternalServerError)
				return
			}
		}
		_, _ = io.WriteString(w, "OK")
	}
}

func worker(ctx context.Context, id int, cfg config.Config, q *queue.Queue, svc *bot.Service) {
	for {
		payload, err := q.DequeueInbound(ctx, cfg.BotID, 5*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("worker %d dequeue: %v", id, err)
			continue
		}
		if payload == nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		var up telegram.Update
		if err := json.Unmarshal(payload, &up); err != nil {
			log.Printf("worker %d bad update: %v", id, err)
			continue
		}
		userID := updateUserID(up)
		release := func(context.Context) error { return nil }
		if userID != "" {
			lockRelease, err := q.AcquireLock(ctx, "miogram:userlock:"+userID, cfg.PerUserLockTTL, cfg.PerUserLockWait)
			if err != nil {
				log.Printf("worker %d lock user %s: %v", id, userID, err)
				_ = q.EnqueueInbound(context.Background(), cfg.BotID, payload)
				continue
			}
			release = lockRelease
		}
		if err := svc.Process(ctx, up, cfg.BotID); err != nil {
			log.Printf("worker %d process update %d user %s: %v", id, up.UpdateID, userID, err)
		}
		_ = release(context.Background())
	}
}

func updateUserID(up telegram.Update) string {
	if up.Message != nil && up.Message.From != nil {
		return strconv.FormatInt(up.Message.From.ID, 10)
	}
	if up.EditedMessage != nil && up.EditedMessage.From != nil {
		return strconv.FormatInt(up.EditedMessage.From.ID, 10)
	}
	if up.CallbackQuery != nil {
		return strconv.FormatInt(up.CallbackQuery.From.ID, 10)
	}
	if up.InlineQuery != nil {
		return strconv.FormatInt(up.InlineQuery.From.ID, 10)
	}
	return ""
}

func clientIP(r *http.Request) string {
	// Never trust X-Forwarded-For: it is attacker-controlled. Use the real peer
	// (the reverse proxy when deployed behind one).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isTelegramIP(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	ranges := []struct {
		lo netip.Addr
		hi netip.Addr
	}{
		{netip.MustParseAddr("149.154.160.0"), netip.MustParseAddr("149.154.175.255")},
		{netip.MustParseAddr("91.108.4.0"), netip.MustParseAddr("91.108.7.255")},
	}
	for _, r := range ranges {
		if addr.Compare(r.lo) >= 0 && addr.Compare(r.hi) <= 0 {
			return true
		}
	}
	return false
}

// isPrivateOrLoopback reports whether ip is a trusted local/proxy address. When
// the bot runs behind a reverse proxy, RemoteAddr is the proxy (private), so we
// accept it without a Telegram-range check rather than trusting a spoofable
// X-Forwarded-For header.
func isPrivateOrLoopback(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsUnspecified()
}
