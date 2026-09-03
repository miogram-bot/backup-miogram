package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"miogram/internal/bot"
	"miogram/internal/config"
	"miogram/internal/queue"
	"miogram/internal/storage"
	"miogram/internal/telegram"
)

type Scheduler struct {
	cfg    config.Config
	store  *storage.Store
	tg     *telegram.Client
	redis  *queue.Queue
	botSvc *bot.Service
}

func New(cfg config.Config, store *storage.Store, tg *telegram.Client, q *queue.Queue, svc *bot.Service) *Scheduler {
	return &Scheduler{cfg: cfg, store: store, tg: tg, redis: q, botSvc: svc}
}

func (s *Scheduler) Run(ctx context.Context) {
	mainTicker := time.NewTicker(s.cfg.JobInterval)
	fakeTicker := time.NewTicker(time.Second)
	defer mainTicker.Stop()
	defer fakeTicker.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil {
			log.Printf("jobs failed: %v", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-fakeTicker.C:
				_ = s.connectExpiredSearches(ctx, time.Now().Unix())
				_ = s.endDueFakeChats(ctx, time.Now().Unix())
			case <-mainTicker.C:
				goto nextRun
			}
		}
	nextRun:
	}
}

func (s *Scheduler) RunOnce(ctx context.Context) error {
	now := time.Now().Unix()
	_, _ = s.store.DB().Exec(ctx, `DELETE FROM search WHERE created_at+86400<$1`, now)
	_, _ = s.store.DB().Exec(ctx, `DELETE FROM chatmsgs WHERE created_at+604800<$1`, now)
	_, _ = s.store.DB().Exec(ctx, `UPDATE tictactoe_games g SET status='ended',updated_at=$1 WHERE g.status='active' AND NOT EXISTS (SELECT 1 FROM chats c WHERE c.id=g.chat_id AND c.status='chatting')`, now)
	_, _ = s.store.DB().Exec(ctx, `DELETE FROM tictactoe_games WHERE status<>'active' AND updated_at+86400<$1`, now)
	_, _ = s.store.DB().Exec(ctx, `DELETE FROM chats WHERE status='end' AND ended_at+1800<$1`, now)
	_, _ = s.store.DB().Exec(ctx, `DELETE FROM users WHERE is_fake=true AND step='start' AND created_at+86400*2<$1`, now)
	_, _ = s.store.DB().Exec(ctx, `DELETE FROM notif WHERE status='end'`)
	_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='doing' WHERE type='search' AND status='connecting' AND date+30<$1`, now)
	if err := s.connectExpiredSearches(ctx, now); err != nil {
		return err
	}
	if err := s.endDueFakeChats(ctx, now); err != nil {
		return err
	}
	if err := s.expireSearches(ctx, now, "normal", 60, "anon"); err != nil {
		return err
	}
	if err := s.expireSearches(ctx, now, "gps", 60, "anon;gps;none"); err != nil {
		return err
	}
	if err := s.expireSearches(ctx, now, "province", 60, "province;select"); err != nil {
		return err
	}
	if err := s.inactiveUsers(ctx, now); err != nil {
		return err
	}
	if err := s.expirePayments(ctx, now); err != nil {
		return err
	}
	return s.processCron(ctx)
}

func (s *Scheduler) connectExpiredSearches(ctx context.Context, now int64) error {
	rows, err := s.store.DB().Query(ctx, `SELECT id FROM notif WHERE type='search' AND status='doing' AND date+60<=$1 ORDER BY id LIMIT 20`, now)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		_, realUser, _, err := s.store.CreateFakeChatFromSearch(ctx, id, now)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) && err.Error() != "user is already chatting" && err.Error() != "search is not ready for fake fallback" {
				log.Printf("fake fallback search %d: %v", id, err)
			}
			continue
		}

		// ✅ Send welcome message through routing system
		welcomeKeyboard := [][]bot.Button{
			{{"text": "👀مشاهده پروفایل این مخاطب👤"}},
			{{"text": "🔒 تغییر حالت خصوصی"}, {"text": "🎮 بازی دوز"}},
			{{"text": "پایان چت"}},
		}
		_, err = s.botSvc.SendMessageWithRoutingAndKeyboard(ctx, realUser.UserID,
			"👀 پیدا کردم وصلتون کرد\n\nبه مخاطبت سلام کن 🗣",
			welcomeKeyboard)
		if err != nil {
			log.Printf("fake welcome failed for %s: %v", realUser.UserID, err)
		}

		// ✅ Send profile view notification through routing system (FIXED)
		_, err = s.botSvc.SendMessageWithRouting(ctx, realUser.UserID,
			"🤖 پیام سیستم 👇\n\n\nمخاطب شما «پروفایل میوگرام» شما را مشاهده کرد.",
			nil)
		if err != nil {
			log.Printf("fake profile view failed for %s: %v", realUser.UserID, err)
		}
	}
	return nil
}

func (s *Scheduler) endDueFakeChats(ctx context.Context, now int64) error {
	rows, err := s.store.DB().Query(ctx, `SELECT id,user_id_1,user_id_2 FROM chats WHERE status='chatting' AND is_fake=true AND fake_end_at<=$1 ORDER BY fake_end_at LIMIT 50`, now)
	if err != nil {
		return err
	}
	type dueChat struct {
		id             int64
		realID, fakeID string
	}
	items := []dueChat{}
	for rows.Next() {
		var item dueChat
		if err := rows.Scan(&item.id, &item.realID, &item.fakeID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		fakeUser, err := s.store.UserByID(ctx, item.fakeID)
		if err != nil {
			log.Printf("fake user %s not found: %v", item.fakeID, err)
			continue
		}
		ended, endErr := s.store.EndChat(ctx, item.id, now)
		if endErr != nil {
			log.Printf("end chat %d failed: %v", item.id, endErr)
			continue
		}

		refund := ended.Refund1
		if ended.UserID2 == item.realID {
			refund = ended.Refund2
		}

		text := "چت شما با /user_" + fakeUser.UniqID + " توسط کاربر مقابل قطع شد\n\n" +
			"برای گزارش عدم رعایت قوانین (/help_terms) می‌توانید با لمس 《 🚫 گزارش کاربر 》 در پروفایل، کاربر را گزارش کنید.\n" +
			"🗑 تا ۳۰ دقیقه بعد از اتمام چت می‌توانید با دستور زیر پیام‌های ارسال‌شده را برای هر دو طرف پاک کنید:\n" +
			"/delete_messages_" + fakeUser.UniqID
		if refund > 0 {
			text += fmt.Sprintf("\n💰 تعداد %d سکه به دلیل ناموفق بودن چت به حساب شما بازگشت.", refund)
		}

		// ✅ Send end chat message through routing system
		mainMenuKeyboard := [][]bot.Button{
			{{"text": "🔗 به یه ناشناس وصلم کن!️"}},
			{{"text": "🔍 جستجوی کاربران 🔎"}, {"text": "📍افراد نزدیک"}},
			{{"text": "💰سکه"}, {"text": "👤پروفایل"}, {"text": "👨‍💻پشتیبانی"}},
			{{"text": "🚸 معرفی به دوستان (سکه رایگان)"}},
		}
		_, err = s.botSvc.SendMessageWithRoutingAndKeyboard(ctx, item.realID, text, mainMenuKeyboard)
		if err != nil {
			log.Printf("fake end chat failed for %s: %v", item.realID, err)
		}
	}
	return nil
}

func fakeGenderEmoji(gender string) string {
	if gender == "girl" {
		return "🙍‍♀️"
	}
	return "🙍‍♂️"
}

func (s *Scheduler) expireSearches(ctx context.Context, now int64, content string, ttl int64, callback string) error {
	rows, err := s.store.DB().Query(ctx, `SELECT id,user_id,age FROM notif WHERE type='search' AND content=$1 AND date+$2<$3 AND status='doing'`, content, ttl, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID     int64
		UserID string
		Age    int
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.UserID, &it.Age); err != nil {
			return err
		}
		items = append(items, it)
	}
	for _, it := range items {
		sameAge := ""
		if it.Age != 0 {
			sameAge = "<code>⚠️ فعال بودن جستجوی همسن باعث افزوده شدن فیلتر سن در جستجو می شود و می تواند باعث دیر پیدا شدن (و یا گاهی پیدا نشدن) مخاطب شما شود. </code>\n\n"
		}
		_, _ = s.tg.Call(ctx, "sendMessage", map[string]any{
			"chat_id": it.UserID,
			"text": "😔 متاسفانه کسی رو پیدا نکردم\n\n" + sameAge +
				"میتونی دوباره جستجو کنی 👇\n\n" +
				"<code>    - در قسمت «🔍 جستجوی پیشرفته 🔎»  می تونی افراد هم استانی و نزدیک بدون چت رو پیدا کنی و بهشون درخواست چت بدی !</code>",
			"parse_mode":   "HTML",
			"reply_markup": telegram.JSON(map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "♻️ جستجو دوباره ♻️", "callback_data": callback}}}}),
		})
		_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE id=$1`, it.ID)
	}
	return rows.Err()
}

func (s *Scheduler) inactiveUsers(ctx context.Context, now int64) error {
	rows, err := s.store.DB().Query(ctx, `SELECT id,user_id,balance,latitude,longitude FROM users WHERE is_fake=false AND ($1-last_activity)>259200 LIMIT 200`, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID             int64
		UserID         string
		Balance        int
		Latitude, Long float64
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.UserID, &it.Balance, &it.Latitude, &it.Long); err != nil {
			return err
		}
		items = append(items, it)
	}
	for _, it := range items {
		countBoy, countGirl := 0, 0
		if it.Latitude != 0 {
			_ = s.store.DB().QueryRow(ctx, nearCountSQL("boy"), it.UserID, it.Latitude, it.Long, now).Scan(&countBoy)
			_ = s.store.DB().QueryRow(ctx, nearCountSQL("girl"), it.UserID, it.Latitude, it.Long, now).Scan(&countGirl)
		}
		gift := ""
		newBalance := it.Balance
		if it.Balance < 5 {
			newBalance += 5
			gift = "نگرانت شدم، اگه سکه نیاز داری بهت 5 تا سکه اضافه کردم.\n\n"
		}
		_, _ = s.tg.Call(ctx, "sendMessage", map[string]any{
			"chat_id":    it.UserID,
			"text":       fmt.Sprintf("سلام 🙂✋ \n\n!یه مدتیه به ربات سر نمیزنی؟ نگرانت شدم\n\n%s<code>از وقتی رفتی (تو این 3 روز)، %d تا 🙎‍♂️ پسر و %d تا 🙍‍♀️دختر که #نزدیک تو بودن تو ربات فعالیت کردن !</code>\n\n- راستی دقت کرده بودی میتونی با معرفی هر نفر به ربات 20 تا سکه بگیری؟😍\n\nمنتظر چی هستی همین الان یکی از گزینه هارو انتخاب کن و به جمع خانواده 10 میلیون نفری %s برگرد👇", gift, countBoy, countGirl, s.cfg.BotName),
			"parse_mode": "HTML",
		})
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET last_activity=$2,balance=$3 WHERE user_id=$1`, it.UserID, now, newBalance)
	}
	return rows.Err()
}

func nearCountSQL(gender string) string {
	return `SELECT count(*) FROM users WHERE user_id<>$1 AND latitude<>0 AND gender='` + gender + `' AND ($4-last_activity)<259200 AND (6371 * acos(least(1, greatest(-1, cos(radians($2))*cos(radians(latitude))*cos(radians(longitude)-radians($3))+sin(radians($2))*sin(radians(latitude)))))) < 100`
}

func (s *Scheduler) expirePayments(ctx context.Context, now int64) error {
	rows, err := s.store.DB().Query(ctx, `SELECT id,user_id FROM payments WHERE updated_at+900<$1 AND status IN ('first_level','move_gateway') LIMIT 50`, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID     int64
		UserID string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.UserID); err != nil {
			return err
		}
		items = append(items, it)
	}
	for _, it := range items {
		_, _ = s.tg.Call(ctx, "sendMessage", map[string]any{
			"chat_id": it.UserID,
			"text": "🔻 بنظر میرسد شما دقایقی پیش اقدام  به خرید سکه کرده اید.\n\n" +
				"💠 درصورتی که خرید شما نا موفق بوده است می توانید دوباره خرید خود را انجام دهید، دقت کنید حتما گزینه \"تکمیل فرایند خرید\" را پس از پرداخت بزنید.\n\n" +
				"💠 درصورت خرید ناموفق مبلغی از حساب شما کسر شده است ، تا 24 ساعت آینده توسط بانک بطور خودکاربه حسابتان بازخواهد گشت.\n\n" +
				"💠 برای پیگیری می‌توانید از بخش پشتیبانی ربات تیکت ثبت کنید.\n\n" +
				"💠 درصورتی که هنگام خرید با خطای \"مشتری گرامی دسترسی شما به این صفحه غیر مجاز می باشد\" و یا \"خطا، شما به درستی هدایت نشده اید.\" مواجه شدید ، از صفحه خارج شده و دوباره از طریق لینک های خرید در ربات، اقدام به خرید کنید.  این خطا به دلیل دوبار وارد شدن پشت سر هم شما به درگاه پرداخت است.",
		})
		_, _ = s.store.DB().Exec(ctx, `UPDATE payments SET status='expired' WHERE id=$1`, it.ID)
	}
	return rows.Err()
}

func (s *Scheduler) processCron(ctx context.Context) error {
	var cr storage.Cron
	err := s.store.DB().QueryRow(ctx, `SELECT id,coalesce(user_id,''),coalesce(chat_id,''),coalesce(type,''),coalesce(file_type,''),coalesce(file_id,''),coalesce(text,''),message_id,send_id,max_send_id,message_id_edit,count_members,send_correct,send_failed FROM cron ORDER BY id ASC LIMIT 1`).
		Scan(&cr.ID, &cr.UserID, &cr.ChatID, &cr.Type, &cr.FileType, &cr.FileID, &cr.Text, &cr.MessageID, &cr.SendID, &cr.MaxSendID, &cr.MessageIDEdit, &cr.CountMembers, &cr.SendCorrect, &cr.SendFailed)
	if err != nil {
		return nil
	}
	if cr.SendID >= cr.MaxSendID {
		done := cr.SendCorrect + cr.SendFailed
		percent := 100
		if cr.CountMembers > 0 {
			percent = done * 100 / cr.CountMembers
		}
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM cron WHERE id=$1`, cr.ID)
		_, _ = s.tg.Call(ctx, "editMessageText", map[string]any{
			"chat_id":    cr.UserID,
			"text":       fmt.Sprintf("🔰 وضعیت: اتمام عملیات — %d%%\n\n✅ موفق: %d\n❌ ناموفق: %d\n🔅 کل: [%d/%d]\n\n❗️ در ارسال ناموفق کاربر ربات را بلاک یا حسابش را حذف کرده است.", percent, cr.SendCorrect, cr.SendFailed, done, cr.CountMembers),
			"message_id": cr.MessageIDEdit,
		})
		return nil
	}
	rows, err := s.store.DB().Query(ctx, `SELECT id,user_id FROM users WHERE id>$1 AND id<=$2 ORDER BY id ASC LIMIT 100`, cr.SendID, cr.MaxSendID)
	if err != nil {
		return err
	}
	defer rows.Close()
	lastID := cr.SendID
	for rows.Next() {
		var id int64
		var userID string
		if err := rows.Scan(&id, &userID); err != nil {
			return err
		}
		lastID = id
		ok := false
		if cr.FileType == "forward" {
			resp, _ := s.tg.Call(ctx, "forwardMessage", map[string]any{"chat_id": userID, "from_chat_id": cr.ChatID, "message_id": cr.MessageID})
			ok = resp.Ok
		} else {
			resp, _ := s.tg.Call(ctx, "sendMessage", map[string]any{"chat_id": userID, "text": cr.Text, "parse_mode": "HTML"})
			ok = resp.Ok
		}
		if ok {
			cr.SendCorrect++
		} else {
			cr.SendFailed++
		}
	}
	if lastID == cr.SendID {
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM cron WHERE id=$1`, cr.ID)
		return nil
	}
	_, _ = s.store.DB().Exec(ctx, `UPDATE cron SET send_id=$2,send_correct=$3,send_failed=$4 WHERE id=$1`, cr.ID, lastID, cr.SendCorrect, cr.SendFailed)
	done := cr.SendCorrect + cr.SendFailed
	percent := 0
	if cr.CountMembers > 0 {
		percent = done * 100 / cr.CountMembers
	}
	_, _ = s.tg.Call(ctx, "editMessageText", map[string]any{
		"chat_id":    cr.UserID,
		"text":       fmt.Sprintf("🔰 وضعیت: در حال ارسال ... %d%%\n\n✅ موفق: %d\n❌ ناموفق: %d\n🔅 کل: [%d/%d]\n\n❗️ در ارسال ناموفق کاربر ربات را بلاک یا حسابش را حذف کرده است.", percent, cr.SendCorrect, cr.SendFailed, done, cr.CountMembers),
		"message_id": cr.MessageIDEdit,
	})
	return rows.Err()
}
