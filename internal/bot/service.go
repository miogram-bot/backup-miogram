package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"miogram/internal/config"
	"miogram/internal/fleet"
	"miogram/internal/payments"
	"miogram/internal/queue"
	"miogram/internal/storage"
	"miogram/internal/telegram"
)

type Service struct {
	cfg            config.Config
	store          *storage.Store
	userStore      userCreator
	tg             *telegram.Client
	loc            *time.Location
	helpImageCache map[string]string
	helpImageMu    sync.RWMutex
	redis          *queue.Queue
	fleet          *fleet.Manager
	routing        routingBackend
	payments       *payments.Service
}

func (s *Service) SetPayments(p *payments.Service) {
	s.payments = p
}

func New(cfg config.Config, store *storage.Store, tg *telegram.Client, q *queue.Queue, fl *fleet.Manager) *Service {
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		loc = time.FixedZone("Asia/Tehran", 3*3600+30*60)
	}
	return &Service{
		cfg:            cfg,
		store:          store,
		userStore:      store,
		tg:             tg,
		loc:            loc,
		helpImageCache: make(map[string]string),
		redis:          q,
		fleet:          fl,
		routing:        store,
	}
}

func (s *Service) Process(ctx context.Context, up telegram.Update, botID string) error {
	c, ok := newUpdateContext(up, time.Now().Unix())
	if !ok {
		return nil
	}
	c.BotID = botID

	if s.cfg.IsHelper() && s.fleet != nil && s.fleet.Mode() == fleet.ModeOffPeak {
		return s.bounceToMain(ctx, &c)
	}

	admin, err := s.store.Admin(ctx)
	if err != nil {
		return err
	}
	c.Admin = admin

	user, err := s.store.UserByID(ctx, c.UserID)
	if err == nil {
		c.User = user
		c.refreshStep()
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if s.fleet != nil && s.fleet.Mode() == fleet.ModePeak && s.cfg.BotID == s.cfg.MainBotID {
		if c.User.UserID != "" && c.User.Gender != "" && c.User.Age > 0 && c.User.State != "" {
			return s.sendMigrationToHelper(ctx, &c)
		}
	}

	handled, stop, err := s.handleAuth(ctx, &c)
	if err != nil || stop || handled {
		return err
	}

	if s.shouldRedirectToClassroom(ctx, &c) {
		return s.sendMigrationToHelper(ctx, &c)
	}

	if c.User.UserID == "" {
		return nil
	}
	if err := s.afterUserOnline(ctx, &c); err != nil {
		log.Printf("online hooks failed: %v", err)
	}

	if handled, err = s.handleStatic(ctx, &c); handled || err != nil {
		return err
	}
	if handled, err = s.handleProfile(ctx, &c); handled || err != nil {
		return err
	}
	if c.UserID == s.cfg.AdminID || c.User.Status == "admin" {
		if handled, err = s.handleAdmin(ctx, &c); handled || err != nil {
			return err
		}
	}
	if handled, err = s.handleChat(ctx, &c); handled || err != nil {
		return err
	}
	if handled, err = s.handleStart(ctx, &c); handled || err != nil {
		return err
	}
	if handled, err = s.handleMain(ctx, &c); handled || err != nil {
		return err
	}
	return s.unknown(ctx, &c)
}

func (s *Service) reloadUser(ctx context.Context, c *UpdateContext) error {
	u, err := s.store.UserByID(ctx, c.UserID)
	if err != nil {
		return err
	}
	c.User = u
	c.refreshStep()
	return nil
}

func (s *Service) afterUserOnline(ctx context.Context, c *UpdateContext) error {
	if c.Inline != nil {
		return nil
	}
	if err := s.store.UpdateUserActivityWithUsername(ctx, c.UserID, c.Username, c.Now); err != nil {
		return err
	}
	if s.redis != nil && c.UserID != "" && c.BotID != "" {
		assignedHere := false
		if s.routing != nil {
			if ab, aerr := s.routing.AssignedBot(ctx, c.UserID); aerr == nil && ab == c.BotID {
				assignedHere = true
			}
		}
		if s.cfg.IsHelper() && s.fleet != nil && (assignedHere || strings.HasPrefix(c.Text, "/start")) {
			s.fleet.DrainPending(ctx, c.BotID, c.UserID, func(method string, params map[string]any) {
				if _, err := s.tg.Call(ctx, method, params); err != nil {
					log.Printf("pending delivery %s to %s: %v", method, c.UserID, err)
				}
			})
		}
	}
	rows, err := s.store.DB().Query(ctx, `SELECT id,user_id FROM notif WHERE user_id_2=$1 AND reason='onlinenotif'`, c.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type nrow struct {
		id     int64
		userID string
	}
	var items []nrow
	for rows.Next() {
		var n nrow
		if err := rows.Scan(&n.id, &n.userID); err != nil {
			return err
		}
		items = append(items, n)
	}
	for _, n := range items {
		_, _ = s.send(ctx, "sendMessage", map[string]any{
			"chat_id": n.userID,
			"text":    "🔔 هم اکنون کاربر /user_" + c.User.UniqID + " آنلاین است.",
		})
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM notif WHERE id=$1`, n.id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.suggestMigrationIfNeeded(ctx, c)
	return nil
}

func (s *Service) shouldRedirectToClassroom(ctx context.Context, c *UpdateContext) bool {
	if s.cfg.BotID != s.cfg.MainBotID {
		return false
	}
	if s.fleet == nil || s.redis == nil {
		return false
	}
	if s.fleet.Mode() == fleet.ModeOffPeak {
		return false
	}
	if strings.HasPrefix(c.User.Step, "chatting;") {
		return false
	}
	queueLen, err := s.redis.OutboundQueueLen(ctx, s.cfg.MainBotID)
	if err != nil {
		return false
	}
	return queueLen >= int64(s.cfg.PeakQueueLen) || s.fleet.Flooded()
}

func (s *Service) handleClassroomChoice(ctx context.Context, c *UpdateContext) error {
	payload := strings.TrimPrefix(c.Text, "/start classroom_")
	if payload == "" {
		return nil
	}
	if payload != c.UserID {
		return nil
	}
	targetBotID := c.BotID
	if targetBotID == s.cfg.MainBotID {
		return nil
	}
	if s.redis != nil {
		changed, err := s.redis.MoveUserBot(ctx, c.UserID, targetBotID)
		if err != nil {
			return err
		}
		if changed && s.routing != nil {
			if derr := s.routing.UpdateAssignedBot(ctx, c.UserID, targetBotID); derr != nil {
				log.Printf("durable assign %s -> %s: %v", c.UserID, targetBotID, derr)
			}
		}
	}
	if s.fleet != nil && c.UserID != "" {
		for _, src := range []string{s.cfg.MainBotID, targetBotID} {
			s.fleet.DrainPending(ctx, src, c.UserID, func(method string, params map[string]any) {
				if _, derr := s.tg.Call(ctx, method, params); derr != nil {
					log.Printf("classroom pending delivery %s to %s: %v", method, c.UserID, derr)
				}
			})
		}
	}
	_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "✅ شما با موفقیت به ربات کمکی منتقل شدید! اکنون می‌توانید از تمام امکانات ربات استفاده کنید.",
		"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	return err
}

func (s *Service) getHelperIDs() []string {
	helpers := make([]string, 0, len(s.cfg.Helpers))
	for id := range s.cfg.Helpers {
		helpers = append(helpers, id)
	}
	sort.Strings(helpers)
	return helpers
}

func (s *Service) bounceToMain(ctx context.Context, c *UpdateContext) error {
	// انتقال کاربر به main در Redis
	if s.redis != nil && c.UserID != "" {
		if _, err := s.redis.MoveUserBot(ctx, c.UserID, s.cfg.MainBotID); err != nil {
			log.Printf("bounce %s to main: %v", c.UserID, err)
		}
	}

	// انتقال کاربر به main در PostgreSQL
	if s.routing != nil && c.UserID != "" {
		if err := s.routing.UpdateAssignedBot(ctx, c.UserID, s.cfg.MainBotID); err != nil {
			log.Printf("bounce durable %s: %v", c.UserID, err)
		}
	}

	// پیام انتقال
	mainUsername := strings.TrimPrefix(s.botUsername(s.cfg.MainBotID), "@")
	link := "https://t.me/" + mainUsername
	_, err := s.tg.Call(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": "🤖 پیام سیستم 👇\n\n" +
			"✅ ربات کمکی موقتاً غیرفعال شد و حساب شما به ربات اصلی منتقل شد.\n" +
			"برای ادامه از لینک زیر استفاده کنید 👇",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{{urlButton("🏠 رفتن به ربات اصلی", link)}})),
	})
	if c.CQID != "" {
		_, _ = s.tg.Call(ctx, "answerCallbackQuery", map[string]any{
			"callback_query_id": c.CQID,
			"text":              "به ربات اصلی منتقل شدید",
		})
	}
	return err
}

func (s *Service) userIsChatting(ctx context.Context, userID string) bool {
	if userID == "" || s.store == nil {
		return false
	}
	var step string
	err := s.store.DB().QueryRow(ctx,
		`SELECT step FROM users WHERE user_id=$1`, userID).Scan(&step)
	if err != nil {
		return false
	}
	return strings.HasPrefix(step, "chatting;")
}

type routingBackend interface {
	AssignedBot(ctx context.Context, userID string) (string, error)
	UpdateAssignedBot(ctx context.Context, userID, botID string) error
}

type userCreator interface {
	CreateUser(ctx context.Context, userID, referral string, now int64) (storage.User, error)
	UserByID(ctx context.Context, userID string) (storage.User, error)
}

func (s *Service) registerBotForUser(ctx context.Context, userID, botID string) (bool, error) {
	if s.redis == nil || userID == "" || botID == "" {
		return false, nil
	}
	changed, err := s.redis.RegisterUserBot(ctx, userID, botID)
	if err != nil {
		return false, err
	}
	if changed && s.routing != nil {
		if err := s.routing.UpdateAssignedBot(ctx, userID, botID); err != nil {
			log.Printf("durable assigned_bot update %s -> %s failed: %v", userID, botID, err)
		}
	}
	return changed, nil
}

func (s *Service) resolveDelivery(ctx context.Context, userID string) (string, error) {
	currentBot := s.cfg.BotID
	if userID == "" {
		return "", fmt.Errorf("resolve delivery: empty user id")
	}

	if s.redis != nil {
		if botID, err := s.redis.GetUserBot(ctx, userID); err != nil {
			log.Printf("resolver redis read %s: %v", userID, err)
		} else if botID != "" {
			// فقط در OFFPEAK کاربر را به main منتقل کن
			if botID != "" && botID != s.cfg.MainBotID {
				if s.fleet != nil && s.fleet.Mode() == fleet.ModeOffPeak {
					log.Printf("resolver offpeak: moving %s from %s to main", userID, botID)
					if _, err := s.redis.MoveUserBot(ctx, userID, s.cfg.MainBotID); err != nil {
						log.Printf("resolver offpeak move redis %s: %v", userID, err)
					}
					if s.routing != nil {
						if err := s.routing.UpdateAssignedBot(ctx, userID, s.cfg.MainBotID); err != nil {
							log.Printf("resolver offpeak move durable %s: %v", userID, err)
						}
					}
					return s.cfg.MainBotID, nil
				}
				return botID, nil
			}
			return s.deliverVia(ctx, userID, botID)
		}
	}

	if s.routing != nil {
		pgBot, err := s.routing.AssignedBot(ctx, userID)
		switch {
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			log.Printf("resolver pg read %s: %v", userID, err)
		case err == nil && pgBot != "":
			if _, cerr := s.registerBotForUser(ctx, userID, pgBot); cerr != nil {
				log.Printf("resolver backfill %s -> %s: %v", userID, pgBot, cerr)
			}
			return s.deliverVia(ctx, userID, pgBot)
		case err == nil && pgBot == "":
			if _, cerr := s.registerBotForUser(ctx, userID, currentBot); cerr != nil {
				log.Printf("resolver backfill %s -> %s: %v", userID, currentBot, cerr)
			}
			return s.deliverVia(ctx, userID, currentBot)
		}
	}

	if s.userStore != nil {
		user, err := s.userStore.UserByID(ctx, userID)
		if err == nil {
			_ = user
			if _, cerr := s.registerBotForUser(ctx, userID, currentBot); cerr != nil {
				log.Printf("resolver backfill %s -> %s: %v", userID, currentBot, cerr)
			}
			return s.deliverVia(ctx, userID, currentBot)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("resolve delivery: load user %s: %w", userID, err)
		}
	}

	if s.userStore == nil {
		return "", fmt.Errorf("resolve delivery: cannot create user %s: no store configured", userID)
	}
	if _, cerr := s.userStore.CreateUser(ctx, userID, "", time.Now().Unix()); cerr != nil {
		return "", fmt.Errorf("resolve delivery: create user %s: %w", userID, cerr)
	}
	if _, cerr := s.registerBotForUser(ctx, userID, currentBot); cerr != nil {
		log.Printf("resolver register %s -> %s: %v", userID, currentBot, cerr)
	}
	return s.deliverVia(ctx, userID, currentBot)
}

func (s *Service) deliverVia(ctx context.Context, userID, botID string) (string, error) {
	mainBot := s.cfg.MainBotID
	if botID == "" || botID == mainBot {
		return mainBot, nil
	}
	if s.fleet != nil && s.fleet.IsActive(botID) {
		return botID, nil
	}
	if s.userIsChatting(ctx, userID) {
		return mainBot, nil
	}
	changed, err := s.redis.MoveUserBot(ctx, userID, mainBot)
	if err != nil {
		log.Printf("resolver fallback %s -> %s: %v", userID, mainBot, err)
	}
	if changed && s.routing != nil {
		if err := s.routing.UpdateAssignedBot(ctx, userID, mainBot); err != nil {
			log.Printf("resolver fallback durable %s: %v", userID, err)
		}
	}
	if changed && s.cfg.MigrationNotify {
		s.notifyMovedBack(ctx, userID)
	}
	return mainBot, nil
}

func (s *Service) notifyMovedBack(ctx context.Context, userID string) {
	ok, err := s.redis.Client().SetNX(ctx, "fleet:movednotice:"+userID, 1, s.cfg.SuggestCooldown).Result()
	if err != nil || !ok {
		return
	}
	mainUsername := strings.TrimPrefix(s.botUsername(s.cfg.MainBotID), "@")
	_ = telegram.EnqueueOutbound(ctx, s.redis.Client(), s.cfg.MainBotID, "sendMessage", map[string]any{
		"chat_id": userID,
		"text": "🤖 پیام سیستم 👇\n\n" +
			"✅ ربات قبلی موقتاً غیرفعال بود؛ گفتگوی شما به ربات اصلی منتقل شد.\n" +
			"https://t.me/" + mainUsername,
		"disable_web_page_preview": true,
	}, s.cfg.OutboundShardCount)
}

func (s *Service) sendMigrationToHelper(ctx context.Context, c *UpdateContext) error {
	if s.fleet == nil || len(s.cfg.Helpers) == 0 {
		return nil
	}
	helper := s.fleet.AssignHelper(c.UserID)
	username := s.fleet.HelperUsername(helper)
	if username == "" {
		return nil
	}
	link := "https://t.me/" + strings.TrimPrefix(username, "@") + "?start=classroom_" + c.UserID
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": "🤖 پیام سیستم 👇\n\n" +
			"⚠️ ربات اصلی در ساعات پرترافیک قرار دارد. برای ادامه بدون وقفه و دریافت پیام‌ها از ربات کمکی استفاده کنید 👇",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{urlButton("🚀 انتقال به ربات کمکی", link)},
		})),
	})
	return err
}

const fromUserParam = "_from_user"

func (s *Service) send(ctx context.Context, method string, params map[string]any) (telegram.APIResponse, error) {
	fromUser, _ := params[fromUserParam].(string)
	delete(params, fromUserParam)
	chatID, _ := params["chat_id"].(string)
	isPrivate := chatID != "" && !strings.HasPrefix(chatID, "-")
	var resp telegram.APIResponse
	var err error
	if isPrivate {
		targetBot, rerr := s.resolveDelivery(ctx, chatID)
		if rerr != nil {
			return telegram.APIResponse{}, rerr
		}
		if targetBot != s.cfg.BotID {
			resp, err = s.tg.CallViaBot(ctx, targetBot, method, params)
		} else {
			resp, err = s.tg.Call(ctx, method, params)
		}
	} else {
		resp, err = s.tg.Call(ctx, method, params)
	}
	if err == nil && isPrivate && isSendMethod(method) {
		s.handleUndeliverable(ctx, chatID, fromUser, method, params, resp)
	}
	return resp, err
}

func (s *Service) handleUndeliverable(ctx context.Context, userID, fromUserID, method string, params map[string]any, resp telegram.APIResponse) {
	if resp.Ok || !resp.NeedsStart() || resp.PermanentlyUndeliverable() {
		return
	}
	if s.redis == nil || s.fleet == nil {
		return
	}
	targetBot, err := s.resolveDelivery(ctx, userID)
	if err != nil || targetBot == "" || targetBot == s.cfg.MainBotID {
		if err != nil {
			log.Printf("pending classification for %s skipped: %v", userID, err)
		}
		return
	}
	if err := s.fleet.EnqueuePending(ctx, targetBot, userID, fromUserID, method, params); err != nil {
		log.Printf("enqueue pending for %s on %s: %v", userID, targetBot, err)
		return
	}
	username := strings.TrimPrefix(s.botUsername(targetBot), "@")
	ok, err := s.redis.Client().SetNX(ctx, "fleet:startalert:"+targetBot+":"+userID, 1, s.cfg.SuggestCooldown).Result()
	if err != nil || !ok {
		return
	}
	alert := map[string]any{
		"chat_id": userID,
		"text": "🤖 پیام سیستم 👇\n\n" +
			"📬 شما پیام‌های در انتظار دارید!\n" +
			"لطفاً ابتدا ربات کمکی را استارت کنید تا پیام‌ها برای شما ارسال شود 👇",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{{urlButton("📨 دریافت پیام‌های در انتظار", "https://t.me/"+username+"?start=classroom_"+userID)}})),
	}
	if _, err := s.tg.CallViaBot(ctx, s.cfg.MainBotID, "sendMessage", alert); err != nil {
		log.Printf("send start alert via main to %s: %v", userID, err)
	}
}

func isSendMethod(method string) bool {
	return strings.HasPrefix(method, "send") || method == "forwardMessage"
}

func (s *Service) suggestMigrationIfNeeded(ctx context.Context, c *UpdateContext) {
	if s.fleet == nil || s.redis == nil || s.cfg.IsHelper() {
		return
	}
	if s.fleet.Mode() != fleet.ModePeak || len(s.cfg.Helpers) == 0 {
		return
	}
	if strings.HasPrefix(c.User.Step, "chatting;") {
		return
	}
	ok, err := s.redis.Client().SetNX(ctx, "fleet:suggest:"+c.UserID, 1, s.cfg.SuggestCooldown).Result()
	if err != nil || !ok {
		return
	}
	if _, err := s.fleet.SuggestMigration(ctx, func(method string, params map[string]any) error {
		_, err := s.tg.Call(ctx, method, params)
		return err
	}, c.UserID); err != nil {
		log.Printf("suggest migration for %s: %v", c.UserID, err)
	}
}

func (s *Service) botUsername(botID string) string {
	if botID == s.cfg.MainBotID {
		return "@" + s.cfg.MainBotUsername
	}
	if s.fleet != nil {
		if u := s.fleet.HelperUsername(botID); u != "" {
			return u
		}
	}
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

func (s *Service) StartLoadMonitoring(ctx context.Context, q *queue.Queue) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if q == nil {
				continue
			}
			queueLen, err := q.OutboundQueueLen(ctx, s.cfg.BotID)
			if err != nil {
				log.Printf("load monitor: %v", err)
				continue
			}
			score := int((queueLen + 9) / 10)
			if err := q.UpdateBotLoad(ctx, s.cfg.BotID, score); err != nil {
				log.Printf("update bot load: %v", err)
			}
		}
	}
}

func (s *Service) maybeRedirectNewUser(ctx context.Context, c *UpdateContext) (bool, error) {
	if c.User.UserID != "" || s.fleet == nil || s.redis == nil || s.cfg.IsHelper() {
		return false, nil
	}
	if len(s.cfg.Helpers) == 0 {
		return false, nil
	}
	if s.fleet.Mode() == fleet.ModePeak {
		return false, nil
	}
	if !s.fleet.Flooded() {
		return false, nil
	}
	targetBot := s.fleet.AssignHelper(c.UserID)
	targetUsername := strings.TrimPrefix(s.botUsername(targetBot), "@")
	if targetBot == "" || targetUsername == "" {
		return false, nil
	}
	link := "https://t.me/" + targetUsername + "?start=classroom_" + c.UserID
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text":    "⚠️ ربات اصلی موقتاً شلوغ است. برای ادامه از ربات زیر استفاده کنید 👇",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{urlButton("🚀 ادامه", link)},
		})),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) unknown(ctx context.Context, c *UpdateContext) error {
	if c.Inline != nil {
		return nil
	}
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":             c.UserID,
		"text":                "متوجه نشدم :/\n\n<code>چه کاری برات انجام بدم؟ از منوی پایین انتخاب کن 👇</code>",
		"parse_mode":          "HTML",
		"reply_to_message_id": c.MessageID,
		"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
	return err
}

func (s *Service) isAdmin(c *UpdateContext) bool {
	return c.UserID == s.cfg.AdminID || c.User.Status == "admin"
}

func (s *Service) mainMenu(ctx context.Context, c *UpdateContext, replyTo bool) error {
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
	params := map[string]any{
		"chat_id":      c.UserID,
		"text":         mainMenuText(),
		"parse_mode":   "HTML",
		"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	}
	if replyTo {
		params["reply_to_message_id"] = c.MessageID
	}
	_, err := s.send(ctx, "sendMessage", params)
	return err
}

func (s *Service) sendMessage(ctx context.Context, chatID, text string, replyTo int, textBtn string) (telegram.APIResponse, error) {
	params := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "html",
		"disable_web_page_preview": true,
	}
	if replyTo != 0 {
		params["reply_to_message_id"] = replyTo
	}
	if textBtn != "" {
		params["reply_markup"] = telegram.JSON(replyMarkupKeyboard([][]button{{textButton(textBtn)}}))
	}
	return s.send(ctx, "sendMessage", params)
}

func (s *Service) SendMessageWithRouting(ctx context.Context, userID, text string, replyMarkup map[string]any) (telegram.APIResponse, error) {
	params := map[string]any{
		"chat_id":                  userID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}
	return s.send(ctx, "sendMessage", params)
}

func (s *Service) SendMessageWithRoutingAndKeyboard(ctx context.Context, userID, text string, keyboard [][]button) (telegram.APIResponse, error) {
	markup := replyMarkupKeyboard(keyboard)
	return s.SendMessageWithRouting(ctx, userID, text, markup)
}

func (s *Service) answer(ctx context.Context, c *UpdateContext, text string) error {
	if c.CQID == "" {
		return nil
	}
	_, err := s.send(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": c.CQID,
		"text":              text,
		"show_alert":        true,
	})
	return err
}

func (s *Service) ack(ctx context.Context, c *UpdateContext) error {
	if c.CQID == "" {
		return nil
	}
	_, err := s.send(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": c.CQID})
	return err
}

func (s *Service) deleteMessage(ctx context.Context, chatID string, messageID int) {
	if chatID == "" || messageID == 0 {
		return
	}
	_, _ = s.send(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID})
}

func (s *Service) asset(name string) any {
	path := filepath.Join(s.cfg.FilesDir, name)
	if _, err := os.Stat(path); err == nil {
		return telegram.LocalFile{Path: path}
	}
	return path
}

func (s *Service) fileURL(name string) string {
	return ""
}

var helpAssetNames = []string{
	"help_chat.jpg", "help_credit.jpg", "help_gps.jpg", "help_profile.jpg",
	"help_sendchat.jpg", "help_direct.jpg", "help_shortcuts.jpg", "help_onw.jpg",
	"ghavanin.jpg", "gateway.jpg",
}

func (s *Service) WarmHelpAssets(ctx context.Context) error {
	admin, err := s.store.Admin(ctx)
	if err != nil {
		return err
	}
	cacheChatID := admin.ChCacheID
	if cacheChatID == "" {
		cacheChatID = s.cfg.AdminID
	}
	for _, name := range helpAssetNames {
		var fileID string
		assetKey := s.cfg.BotID + ":" + name
		err := s.store.DB().QueryRow(ctx, `SELECT file_id FROM telegram_assets WHERE name=$1`, assetKey).Scan(&fileID)
		if err == nil && fileID != "" {
			s.cacheHelpImage(name, fileID)
			continue
		}
		resp, sendErr := s.send(ctx, "sendPhoto", map[string]any{
			"chat_id": cacheChatID,
			"photo":   localAsset(s.cfg.FilesDir, name),
			"caption": "cache:" + name,
		})
		if sendErr != nil || !resp.Ok {
			if sendErr != nil {
				return sendErr
			}
			return fmt.Errorf("cache help asset %s: %s", name, resp.Description)
		}
		msg, ok := s.tg.SentMessage(resp)
		if !ok || len(msg.Photo) == 0 {
			return fmt.Errorf("cache help asset %s: Telegram returned no photo id", name)
		}
		fileID = msg.Photo[len(msg.Photo)-1].FileID
		if _, err := s.store.DB().Exec(ctx, `INSERT INTO telegram_assets (name,file_id,updated_at) VALUES ($1,$2,$3) ON CONFLICT (name) DO UPDATE SET file_id=excluded.file_id,updated_at=excluded.updated_at`, assetKey, fileID, time.Now().Unix()); err != nil {
			return err
		}
		s.cacheHelpImage(name, fileID)
	}
	return nil
}

func (s *Service) cacheHelpImage(name, fileID string) {
	s.helpImageMu.Lock()
	s.helpImageCache[name] = fileID
	s.helpImageMu.Unlock()
}

func (s *Service) helpImage(ctx context.Context, name string) any {
	s.helpImageMu.RLock()
	fileID := s.helpImageCache[name]
	s.helpImageMu.RUnlock()
	if fileID != "" {
		return fileID
	}
	if s.store.DB().QueryRow(ctx, `SELECT file_id FROM telegram_assets WHERE name=$1`, s.cfg.BotID+":"+name).Scan(&fileID) == nil && fileID != "" {
		s.cacheHelpImage(name, fileID)
		return fileID
	}
	return localAsset(s.cfg.FilesDir, name)
}

func (s *Service) paymentURL(paymentID int64) string {
	base := s.cfg.PublicBaseURL
	if base == "" {
		base = ""
	}
	return strings.TrimRight(base, "/") + "/pay/request.php?id=" + strconv.FormatInt(paymentID, 10)
}

func (s *Service) defaultProfilePhoto(gender string) any {
	if gender != "girl" {
		gender = "boy"
	}
	return s.asset("noimage-" + gender + ".jpg")
}

func (s *Service) profileUsersDir() string {
	return filepath.Join(s.cfg.FilesDir, "..", "profile-users")
}

func (s *Service) profilePhotoPath(userID string) string {
	return filepath.Join(s.profileUsersDir(), "user_"+userID+".jpg")
}

func (s *Service) downloadAndSaveProfilePhoto(ctx context.Context, fileID, userID string) string {
	if fileID == "" {
		return ""
	}
	info, err := s.tg.GetFile(ctx, fileID)
	if err != nil {
		log.Printf("profile photo getFile %s: %v", userID, err)
		return ""
	}
	data, err := s.tg.DownloadFile(ctx, info.FilePath)
	if err != nil {
		log.Printf("profile photo download %s: %v", userID, err)
		return ""
	}
	dir := s.profileUsersDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("profile photo mkdir %s: %v", dir, err)
		return ""
	}
	path := s.profilePhotoPath(userID)
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("profile photo write %s: %v", path, err)
		return ""
	}
	return path
}

func (s *Service) userProfilePhoto(ctx context.Context, user storage.User) any {
	path := s.profilePhotoPath(user.UserID)
	if _, err := os.Stat(path); err == nil {
		return telegram.LocalFile{Path: path}
	}

	if user.IsFake && user.Image != "" {
		return user.Image
	}

	return s.defaultProfilePhoto(user.Gender)
}

func (s *Service) completeProfile(ctx context.Context, user storage.User) (count int, info string, inline [][]button) {
	missing := []string{}
	if user.Name == "" {
		missing = append(missing, "نام")
		inline = [][]button{{callbackButton("👤 تکمیل پروفایـــل", "profile;name;complete")}}
	}
	if user.City == "" {
		if len(inline) == 0 {
			inline = [][]button{{callbackButton("👤 تکمیل پروفایـــل", "profile;city;complete")}}
		}
		missing = append(missing, "شهر")
	}
	if user.Latitude == 0 {
		if len(inline) == 0 {
			inline = [][]button{{callbackButton("👤 تکمیل پروفایـــل", "profile;gps;complete")}}
		}
		missing = append(missing, "موقعیت مکانی")
	}
	return len(missing), strings.Join(missing, " , "), inline
}

func (s *Service) checkProfileCoin(ctx context.Context, c *UpdateContext) (bool, error) {
	user, err := s.store.UserByID(ctx, c.UserID)
	if err != nil {
		return false, err
	}
	c.User = user
	c.refreshStep()

	if user.Name == "" || user.City == "" || user.Latitude == 0 {
		if user.PrevStep == "complete_profile" {
			return false, s.handleProfileCallback(ctx, c)
		}
		return false, nil
	}

	if user.IsCoinComplete {
		if err := s.store.UpdateUserStepPrev(ctx, c.UserID, user.Step, "start"); err != nil {
			return false, err
		}
		c.User.PrevStep = "start"
		return false, nil
	}
	if _, err := s.store.DB().Exec(ctx, `UPDATE users SET balance=balance+$2,is_coin_comprof=true,prev_step='start' WHERE user_id=$1`, c.UserID, c.Admin.CoinCompleteProfile); err != nil {
		return false, err
	}
	c.User.PrevStep = "start"
	user.Balance += c.Admin.CoinCompleteProfile
	_, _ = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": fmt.Sprintf("🔔 تبریک !\n\nشما %d سکه بابت تکمیل کردن پروفایل دریافت کردید !\n\n💰سکه فعلی شما : %d",
			c.Admin.CoinCompleteProfile, user.Balance),
		"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	if user.Referral != "" {
		_ = s.store.AddBalance(ctx, user.Referral, c.Admin.CoinPerInviteProfile)
		ref, err := s.store.UserByID(ctx, user.Referral)
		if err == nil {
			_, _ = s.send(ctx, "sendMessage", map[string]any{
				"chat_id": ref.UserID,
				"text": fmt.Sprintf("🔔 تبریک ! شما %d سکه بابت تکمیل شدن پروفایل کاربری که توسط شما معرفی شده بود دریافت کردید.\n\n💰سکه فعلی شما : %d",
					c.Admin.CoinPerInviteProfile, ref.Balance+c.Admin.CoinPerInviteProfile),
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("👥 معرفی افراد بیشتر", "invite")}})),
			})
		}
	}
	return true, nil
}

func (s *Service) userInfoList(ctx context.Context, c *UpdateContext, rows []struct {
	UserID string
	Name   string
}, start int) (string, error) {
	var b strings.Builder
	for i, row := range rows {
		user, err := s.store.UserByID(ctx, row.UserID)
		if err != nil {
			continue
		}
		b.WriteString(userInfoLine(c.Now, c.User, user, start+i, row.Name))
	}
	return b.String(), nil
}

func (s *Service) listShow(ctx context.Context, c *UpdateContext, text, markup string, total, page, step int, first, end []button) error {
	prev := page - 1
	next := page + 1
	inline := [][]button{}
	if first != nil {
		inline = append(inline, first)
	}
	isCallbackPage := c.Callback != nil && part(c.ExData, len(c.ExData)-1) != "none"
	if !isCallbackPage {
		inline = append(inline, []button{callbackButton("➡️ مشاهده ادامه لیست", markup+";2")})
		if end != nil {
			inline = append(inline, end)
		}
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":                  c.UserID,
			"text":                     text,
			"reply_markup":             telegram.JSON(replyMarkupInline(inline)),
			"parse_mode":               "html",
			"disable_web_page_preview": true,
		})
		return err
	}
	if step >= total {
		return s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
	}
	if page == 1 {
		inline = append(inline, []button{callbackButton("➡️ مشاهده ادامه لیست", fmt.Sprintf("%s;%d", markup, next))})
	} else if total > page*step {
		inline = append(inline, []button{callbackButton("لیست قبلی ⬅️", fmt.Sprintf("%s;%d", markup, prev)), callbackButton("➡️ لیست بعدی", fmt.Sprintf("%s;%d", markup, next))})
	} else {
		inline = append(inline, []button{callbackButton("لیست قبلی ⬅️", fmt.Sprintf("%s;%d", markup, prev))})
	}
	if end != nil {
		inline = append(inline, end)
	}
	_, err := s.send(ctx, "editMessageText", map[string]any{
		"chat_id":                  c.UserID,
		"text":                     text,
		"reply_markup":             telegram.JSON(replyMarkupInline(inline)),
		"message_id":               c.MessageID,
		"parse_mode":               "html",
		"disable_web_page_preview": true,
	})
	return err
}

func (s *Service) isMember(ctx context.Context, channel, userID string) string {
	resp, err := s.send(ctx, "getChatMember", map[string]any{"chat_id": channel, "user_id": userID})
	if err != nil || !resp.Ok {
		return "left"
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "left"
	}
	return result.Status
}
