// Package bot
// توجه: رویه ثبت تصویر پروفایل تغییر کرده است. تصاویر بدون نیاز به تأیید ادمین
// مستقیماً ذخیره می‌شوند و تنها یک دکمه "بن کاربر" برای ادمین ارسال می‌گردد.
// تابع adminBanUser این دکمه را مدیریت می‌کند.
package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"miogram/internal/telegram"
)

func (s *Service) handleAdmin(ctx context.Context, c *UpdateContext) (bool, error) {
	if strings.HasPrefix(c.Text, "/reverify") {
		id := parseInt64(strings.TrimSpace(strings.TrimPrefix(c.Text, "/reverify")))
		if id == 0 {
			return true, s.answer(ctx, c, "استفاده: /reverify <payment_id>")
		}
		if s.payments == nil {
			return true, s.answer(ctx, c, "سرویس پرداخت در دسترس نیست.")
		}
		if err := s.payments.ReVerifyPayment(ctx, id); err != nil {
			return true, s.answer(ctx, c, "خطا در بررسی مجدد: "+err.Error())
		}
		return true, s.answer(ctx, c, "✅ پرداخت مجدداً بررسی و در صورت موفقیت واریز شد.")
	}
	if part(c.ExData, 0) == "ticket_reply" {
		return true, s.startTicketReply(ctx, c)
	}
	if part(c.ExData, 0) == "profile_ban" {
		return true, s.adminBanUser(ctx, c)
	}	
	if part(c.ExData, 0) == "card_review" {
		return true, s.adminReviewCardReceipt(ctx, c)
	}
	if part(c.ExStep, 0) == "ticket_reply" {
		return true, s.submitTicketReply(ctx, c)
	}
	if c.Text == "/panel" || c.Text == "👤 پنل ادمین 👤" || c.Text == PanelButton {
		_ = s.store.UpdateUserStep(ctx, c.UserID, "panel")
		return true, s.panel(ctx, c, "admin")
	}
	if (part(c.ExText, 0) == "/d" && part(c.ExText, 1) != "") || (part(c.ExData, 0) == "d" && part(c.ExData, 1) != "") {
		return true, s.adminUserDetail(ctx, c)
	}
	if c.Text == "📊 آمار" {
		return true, s.adminStats(ctx, c)
	}
	if c.Text == "💳 تراکنش ها" || part(c.ExData, 0) == "paysListAdmin" {
		return true, s.adminPaymentsList(ctx, c)
	}
	if c.Text == "👥 کاربران" {
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "users;none", "panel")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text":    "پنل ادمین » کاربران:",
			"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{
				{textButton("👥 ادمین"), textButton("👥 بلاک"), textButton("👥 همه")},
				{textButton("👥 پیام همگانی"), textButton("👥 فوروارد همگانی")},
				{textButton("👥 سکه همگانی")},
				{textButton(PanelButton)},
			})),
		})
		return true, err
	}
	if part(c.ExStep, 0) == "users" {
		if handled, err := s.adminUsersStep(ctx, c); handled || err != nil {
			return handled, err
		}
	}
	if c.Text == "💰 تعرفه ها" {
		return true, s.panel(ctx, c, "tariff")
	}
	if part(c.ExStep, 0) == "set_vip" {
		return true, s.adminTariffStep(ctx, c)
	}
	if part(c.ExText, 0) == "/p" {
		return true, s.adminPaymentDetail(ctx, c)
	}
	if c.Text == "⚙️ تنظیمات" {
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "settings;none", "panel")
		return true, s.panel(ctx, c, "settings")
	}
	if part(c.ExStep, 0) == "settings" {
		return true, s.adminSettingsStep(ctx, c)
	}
	if part(c.ExText, 0) == "/dlchj" {
		ch := "channel_" + part(c.ExText, 1)
		if ch == "channel_1" || ch == "channel_2" || ch == "channel_3" {
			_, _ = s.store.DB().Exec(ctx, `UPDATE admin SET `+ch+`=NULL`)
			s.store.InvalidateAdmin(ctx)
			_, _ = s.sendMessage(ctx, c.UserID, "✅ انجام شد.", 0, "")
			return true, s.panel(ctx, c, "settings")
		}
	}
	if part(c.ExStep, 0) == "sendmsg" {
		return true, s.adminSendMessageStep(ctx, c)
	}
	if part(c.ExText, 0) == "/block" || part(c.ExText, 0) == "/unblock" || part(c.ExText, 0) == "/addAdmin" || part(c.ExText, 0) == "/delAdmin" {
		return true, s.adminStatusCommand(ctx, c)
	}
	if c.Text == "👥 ادمین" || part(c.ExData, 0) == "adminListAdmin" {
		return true, s.adminSimpleUserList(ctx, c, "admin", "adminListAdmin", "🔰 لیست ادمین", "/delAdmin_")
	}
	if c.Text == "👥 همه" || part(c.ExData, 0) == "usersListAdmin" {
		return true, s.adminAllUsersList(ctx, c)
	}
	if c.Text == "👥 بلاک" || part(c.ExData, 0) == "blockListAdmin" {
		return true, s.adminSimpleUserList(ctx, c, "block", "blockListAdmin", "🔰 لیست کاربران:", "/unblock")
	}
	return false, nil
}

func (s *Service) adminGroupID(ctx context.Context) string {
	admin, err := s.store.Admin(ctx)
	if err != nil {
		return ""
	}
	return admin.AdminGroupID
}

func (s *Service) adminBanUser(ctx context.Context, c *UpdateContext) error {
    userID := part(c.ExData, 1)
    if userID == "" {
        return s.answer(ctx, c, "کاربر مشخص نشده است.")
    }

    // ۱. کاربر را بلاک کن
    _, err := s.store.DB().Exec(ctx, `UPDATE users SET status='block' WHERE user_id=$1`, userID)
    if err != nil {
        return s.answer(ctx, c, "خطا در بن کاربر.")
    }

    // ۲. به کاربر اطلاع بده
    _, _ = s.send(ctx, "sendMessage", map[string]any{
        "chat_id": userID,
        "text":    "⛔️ حساب شما توسط مدیریت ربات مسدود شد. برای پیگیری می‌توانید از بخش پشتیبانی تیکت ثبت کنید.",
    })

    // ۳. ویرایش پیام ادمین (حذف دکمه و نشان دادن وضعیت)
    _, err = s.send(ctx, "editMessageCaption", map[string]any{
        "chat_id":    c.ChatID,
        "message_id": c.MessageID,
        "caption":    "🚫 کاربر بن شد.",
        "reply_markup": telegram.JSON(replyMarkupInline([][]button{})), // دکمه‌ها حذف شوند
    })
    return err
}

func getUserUniq(ctx context.Context, s *Service, userID string) string {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return userID
	}
	return u.UniqID
}

func getUserUsername(ctx context.Context, s *Service, userID string) string {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return ""
	}
	if u.Username != "" {
		return u.Username
	}
	return ""
}

func (s *Service) adminReviewCardReceipt(ctx context.Context, c *UpdateContext) error {
	action := part(c.ExData, 1)
	paymentID := parseInt64(part(c.ExData, 2))
	if paymentID == 0 || (action != "approve" && action != "reject" && action != "edit") {
		return s.answer(ctx, c, "درخواست نامعتبر است.")
	}

	if action == "edit" {
		// Re-open for review. Successful (already-credited) payments must not be
		// re-opened, otherwise re-approval would credit the user a second time.
		tag, err := s.store.DB().Exec(ctx, `UPDATE payments SET status='card_review',reviewed_by=NULL,reviewed_at=0 WHERE id=$1 AND status IN ('rejected')`, paymentID)
		if err != nil || tag.RowsAffected() == 0 {
			var status string
			_ = s.store.DB().QueryRow(ctx, `SELECT status FROM payments WHERE id=$1`, paymentID).Scan(&status)
			if status == "success" {
				return s.answer(ctx, c, "⚠️ این پرداخت قبلاً تأیید و سکه آن واریز شده است. قابل ویرایش نیست.")
			}
			return s.answer(ctx, c, "این پرداخت قابل ویرایش نیست.")
		}
		// Get payment info to rebuild buttons
		var userID, receiptFileID string
		var coins, amount, trackingNumber int
		_ = s.store.DB().QueryRow(ctx, `SELECT user_id,coalesce(receipt_file_id,''),coins,amount,tracking_number FROM payments WHERE id=$1`, paymentID).Scan(&userID, &receiptFileID, &coins, &amount, &trackingNumber)
		buyerUsername := getUserUsername(ctx, s, userID)
		buyerUniq := getUserUniq(ctx, s, userID)
		caption := fmt.Sprintf("🔢 کد پیگیری: %d\n🤵‍♂️ خریدار :\n%s\n/user_%s\n/user_%s\n💰 مبلغ: %s تومان\n💎 تعداد سکه: %d",
			trackingNumber, buyerUsername, userID, buyerUniq, formatNumber(amount), coins)
		_, editErr := s.send(ctx, "editMessageCaption", map[string]any{
			"chat_id":    c.ChatID,
			"message_id": c.MessageID,
			"caption":    caption,
			"parse_mode": "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("✅ تایید رسید پرداخت", fmt.Sprintf("card_review;approve;%d", paymentID))},
				{callbackButton("❌ رد کردن رسید", fmt.Sprintf("card_review;reject;%d", paymentID))},
			})),
		})
		_ = s.answer(ctx, c, "وضعیت پرداخت برای ویرایش باز شد.")
		return editErr
	}

	var userID, status, receiptFileID string
	var coins int
	var amount, trackingNumber int
	if err := s.store.DB().QueryRow(ctx, `SELECT user_id,coins,amount,status,coalesce(receipt_file_id,''),tracking_number FROM payments WHERE id=$1`, paymentID).Scan(&userID, &coins, &amount, &status, &receiptFileID, &trackingNumber); err != nil || (status != "card_review" && status != "success" && status != "rejected") {
		return s.answer(ctx, c, "این فیش قبلاً بررسی شده یا معتبر نیست.")
	}

	adminUsername := c.UserID
	if c.Username != "" {
		adminUsername = c.Username
	}

	buyerUsername := getUserUsername(ctx, s, userID)
	buyerUniq := getUserUniq(ctx, s, userID)

	if action == "reject" {
		tag, err := s.store.DB().Exec(ctx, `UPDATE payments SET status='rejected',reviewed_by=$2,reviewed_at=$3,updated_at=$3 WHERE id=$1 AND status='card_review'`, paymentID, c.UserID, c.Now)
		if err != nil || tag.RowsAffected() == 0 {
			return s.answer(ctx, c, "این فیش قبلاً بررسی شده است.")
		}
		// Update admin message
		caption := fmt.Sprintf("🔢 کد پیگیری: %d\n🤵‍♂️ خریدار :\n%s\n/user_%s\n/user_%s\n💰 مبلغ: %s تومان\n💎 تعداد سکه: %d\n👮‍♂️ادمین: %s\n🛒 وضعیت: رد شد ❌",
			trackingNumber, buyerUsername, userID, buyerUniq, formatNumber(amount), coins, adminUsername)
		_, _ = s.send(ctx, "editMessageCaption", map[string]any{
			"chat_id":    c.ChatID,
			"message_id": c.MessageID,
			"caption":    caption,
			"parse_mode": "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{{
				callbackButton("تغییر وضعیت پرداخت 🔖", fmt.Sprintf("card_review;edit;%d", paymentID)),
			}})),
		})
		_, _ = s.send(ctx, "sendMessage", map[string]any{
			"chat_id": userID,
			"text":    fmt.Sprintf("❌ فیش پرداخت با کد پیگیری #%d تأیید نشد. برای پیگیری از پشتیبانی استفاده کنید.", trackingNumber),
		})
		return s.answer(ctx, c, "فیش رد شد.")
	}

	// Approve flow
	tx, err := s.store.DB().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Guard against double credit: skip if already credited.
	var alreadyCredited bool
	_ = tx.QueryRow(ctx, `SELECT credited FROM payments WHERE id=$1`, paymentID).Scan(&alreadyCredited)
	if alreadyCredited {
		return s.answer(ctx, c, "⚠️ این پرداخت قبلاً تأیید و سکه آن واریز شده است.")
	}
	tag, err := tx.Exec(ctx, `UPDATE payments SET status='success',ref_id=$2,reviewed_by=$3,reviewed_at=$4,updated_at=$4,credited=true WHERE id=$1 AND status='card_review' AND credited=false`, paymentID, fmt.Sprintf("card-%d", paymentID), c.UserID, c.Now)
	if err != nil || tag.RowsAffected() == 0 {
		return s.answer(ctx, c, "این فیش قبلاً بررسی شده است.")
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET balance=balance+$2 WHERE user_id=$1`, userID, coins); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}

	// Update admin message
	caption := fmt.Sprintf("🔢 کد پیگیری: %d\n🤵‍♂️ خریدار :\n%s\n/user_%s\n/user_%s\n💰 مبلغ: %s تومان\n💎 تعداد سکه: %d\n👮‍♂️ادمین: %s\n🛒 وضعیت: تایید شد ✅",
		trackingNumber, buyerUsername, userID, buyerUniq, formatNumber(amount), coins, adminUsername)
	_, _ = s.send(ctx, "editMessageCaption", map[string]any{
		"chat_id":    c.ChatID,
		"message_id": c.MessageID,
		"caption":    caption,
		"parse_mode": "HTML",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{{
			callbackButton("تغییر وضعیت پرداخت 🔖", fmt.Sprintf("card_review;edit;%d", paymentID)),
		}})),
	})
	_, _ = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": userID,
		"text":    fmt.Sprintf("✅ فیش پرداخت با کد پیگیری #%d تأیید و %d سکه به حساب شما اضافه شد.", trackingNumber, coins),
	})
	return s.answer(ctx, c, "فیش تأیید و سکه واریز شد.")
}

// ==================== تابع panel با تغییرات ====================
func (s *Service) panel(ctx context.Context, c *UpdateContext, typ string) error {
	switch typ {
	case "admin":
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":    c.UserID,
			"text":       "پنل ادمین:\n\n⭕️ جزئیات کاربر:\n/d_" + c.UserID,
			"parse_mode": "HTML",
			"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{
				{textButton("⚙️ تنظیمات"), textButton("👥 کاربران"), textButton("📊 آمار")},
				{textButton("💳 تراکنش ها"), textButton("💰 تعرفه ها")},
				{textButton(MainButton)},
			})),
		})
		return err
	case "settings":
		admin, _ := s.store.Admin(ctx)
		msg := func(username string) string {
			if username == "" {
				return "ثبت نشده"
			}
			return "@" + username
		}
		ch := func(name, username, dl string) string {
			if username == "" {
				return "ثبت نشده"
			}
			return "@" + username + "\nحذف کانال ثبت شده = " + dl
		}
		cacheChannel := "ثبت نشده"
		if admin.ChCacheID != "" {
			cacheChannel = admin.ChCacheID
		}
		adminGroup := "ثبت نشده"
		if admin.AdminGroupID != "" {
			adminGroup = admin.AdminGroupID
		}
		// ====== تعریف متغیر cardInfo در اینجا (تغییر ۱) ======
		cardInfo := "ثبت نشده"
		if admin.CardNumber != "" {
			cardInfo = admin.CardNumber + " — " + admin.CardHolder
		}
		// =====================================================
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "پنل ادمین » تنظیمات:\n\n" +
				"کانال جوین اجباری 1: " + ch(admin.Channel1Name, admin.Channel1, "/dlchj_1") + "\n\n" +
				"کانال جوین اجباری 2: " + ch(admin.Channel2Name, admin.Channel2, "/dlchj_2") + "\n\n" +
				"کانال کش: " + cacheChannel + "\nگروه پشتیبانی: " + msg(admin.Support) + "\nگروه ادمین‌ها: " + adminGroup + "\nکارت: " + cardInfo,
			"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{
				{textButton("⚙️ اجباری 2"), textButton("⚙️ اجباری 1")},
				{textButton("📭 پشتیبانی"), textButton("📭 گروه ادمین")},
				{textButton("🗑 کانال کش")},
				{textButton("💳 تنظیم کارت‌به‌کارت")},
				{textButton(BackButton), textButton(PanelButton)},
			})),
		})
		return err
	case "tariff":
		rows, err := s.store.DB().Query(ctx, `SELECT coin FROM amounts ORDER BY amount ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		kb := [][]button{}
		row := []button{}
		for rows.Next() {
			var coin int
			if err := rows.Scan(&coin); err != nil {
				return err
			}
			row = append(row, textButton(fmt.Sprint(coin)))
			if len(row) == 2 {
				kb = append(kb, row)
				row = []button{}
			}
		}
		if len(row) > 0 {
			kb = append(kb, row)
		}
		kb = append(kb, []button{textButton("افزودن تعرفه ➕")}, []button{textButton(PanelButton)})
		_ = s.store.UpdateUserStep(ctx, c.UserID, "set_vip;check")
		_, err = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "پنل ادمین » تعرفه ها:\n\n❗️ با کلیک بر روی هر کدام آن ها را حذف کنید یا تعرفه جدید اضافه کنید.", "reply_markup": telegram.JSON(replyMarkupKeyboard(kb))})
		return err
	}
	return nil
}

// ============================================================

func (s *Service) adminUserDetail(ctx context.Context, c *UpdateContext) error {
	uid := part(c.ExText, 1)
	if uid == "" {
		uid = part(c.ExData, 1)
	}
	user, err := s.store.UserByUniqOrID(ctx, uid)
	if err != nil {
		_, _ = s.sendMessage(ctx, c.UserID, "❌ یافت نشد!", 0, "")
		return nil
	}
	method := "sendMessage"
	if part(c.ExData, 0) == "d" {
		method = "editMessageText"
		action := part(c.ExData, 2)
		switch action {
		case "sendmsg":
			_ = s.store.UpdateUserStep(ctx, c.UserID, "sendmsg;"+user.UserID)
			_, _ = s.sendMessage(ctx, c.UserID, "📌 پیام خود را وارد کنید:\nکنسل: /panel", 0, "")
			return nil
		case "block", "admin", "user":
			if c.UserID != s.cfg.AdminID {
				if action == "block" && user.Status == "admin" {
					return s.answer(ctx, c, "❌ فقط ادمین اصلی میتونه یک ادمین رو بلاک بکنه!")
				}
				if action == "admin" || (action == "user" && user.Status == "admin") {
					return s.answer(ctx, c, "❌ فقط ادمین اصلی میتونه یک ادمین منصوب/عزل بکنه!")
				}
			}
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET status=$2 WHERE user_id=$1`, user.UserID, action)
			user.Status = action
			if action == "block" {
				_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": user.UserID, "text": "⛔️ حساب شما توسط مدیریت ربات مسدود شد. برای پیگیری می‌توانید از بخش پشتیبانی تیکت ثبت کنید."})
			}
		case "balance":
			delta := parseInt(part(c.ExData, 3))
			if user.Balance+delta < 0 {
				return s.answer(ctx, c, "باید بیشتر از 0 باشد.")
			}
			user.Balance += delta
			_ = s.store.SetBalance(ctx, user.UserID, user.Balance)
		}
	}
	block, blockStatus := "بلاک ❌", "block"
	if user.Status == "block" {
		block, blockStatus = "بلاک ✅", "user"
	}
	admin, adminStatus := "ادمین ❌", "admin"
	if user.Status == "admin" {
		admin, adminStatus = "ادمین ✅", "user"
	}
	today := startOfToday(s.loc).Unix()
	yesterday := today - 86400
	count := func(q string) int {
		var n int
		_ = s.store.DB().QueryRow(ctx, q, user.UserID, today, yesterday).Scan(&n)
		return n
	}
	cbToday := count(`SELECT count(*) FROM blocked WHERE user_id=$1 AND created_at>=$2`)
	cbYesterday := count(`SELECT count(*) FROM blocked WHERE user_id=$1 AND created_at>=$3 AND created_at<$2`)
	cbdToday := count(`SELECT count(*) FROM blocked WHERE target_id=$1 AND created_at>=$2`)
	cbdYesterday := count(`SELECT count(*) FROM blocked WHERE target_id=$1 AND created_at>=$3 AND created_at<$2`)
	params := map[string]any{
		"chat_id": c.UserID,
		"text": fmt.Sprintf("کاربر <a href='tg://user?id=%s'>%s</a> (/d_%s)\n\nآیدی: /user_%s\nبلاک کرده امروز: %d\nبلاک کرده دیروز: %d\nبلاک شده امروز: %d\nبلاک شده دیروز: %d",
			user.UserID, user.UserID, user.UserID, user.UniqID, cbToday, cbYesterday, cbdToday, cbdYesterday),
		"disable_web_page_preview": true,
		"parse_mode":               "html",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{callbackButton("ارسال پیام", "d;"+user.UserID+";sendmsg")},
			{callbackButton(block, "d;"+user.UserID+";"+blockStatus), callbackButton(admin, "d;"+user.UserID+";"+adminStatus)},
			{callbackButton("سکه: "+formatNumber(user.Balance), "test")},
			{callbackButton("-10", "d;"+user.UserID+";balance;-10"), callbackButton("-5", "d;"+user.UserID+";balance;-5"), callbackButton("-1", "d;"+user.UserID+";balance;-1"), callbackButton("+1", "d;"+user.UserID+";balance;+1"), callbackButton("+5", "d;"+user.UserID+";balance;+5"), callbackButton("+10", "d;"+user.UserID+";balance;+10")},
		})),
	}
	if method == "editMessageText" {
		params["message_id"] = c.MessageID
	}
	_, err = s.send(ctx, method, params)
	return err
}

func (s *Service) adminStats(ctx context.Context, c *UpdateContext) error {
	today := startOfToday(s.loc).Unix()
	yesterday := today - 86400
	onlineThreshold := c.Now - 900 // 15 دقیقه قبل

	count := func(q string, args ...any) int {
		var n int
		_ = s.store.DB().QueryRow(ctx, q, args...).Scan(&n)
		return n
	}

	// ---- اطلاعات Fleet ----
	mode := s.redis.FleetMode(ctx)
	if mode == "" {
		mode = "نامشخص"
	}

	var botStats strings.Builder
	bots := []string{"main", "shard1", "shard2", "shard3", "shard4", "shard5"}
	for _, bot := range bots {
		members, err := s.redis.Client().SMembers(ctx, "user_bot_reverse:"+bot).Result()
		if err != nil {
			botStats.WriteString(fmt.Sprintf("%s: ❌ خطا\n", bot))
			continue
		}
		botStats.WriteString(fmt.Sprintf("%s: %d کاربر\n", bot, len(members)))
	}
	// ---------------------------------

	// ---- آنلاین هر ربات ----
	onlinePerBot := make(map[string]int)
	rows, err := s.store.DB().Query(ctx, `
		SELECT COALESCE(assigned_bot, 'main'), COUNT(*)
		FROM users
		WHERE is_fake = false AND last_activity > $1
		GROUP BY COALESCE(assigned_bot, 'main')
	`, onlineThreshold)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var bot string
			var cnt int
			if err := rows.Scan(&bot, &cnt); err == nil {
				onlinePerBot[bot] = cnt
			}
		}
	}

	var onlineStats strings.Builder
	for _, bot := range bots {
		cnt := onlinePerBot[bot]
		onlineStats.WriteString(fmt.Sprintf("%s: %d آنلاین\n", bot, cnt))
	}
	// ---------------------------------

	// ---- جنسیت ----
	var boyCount, girlCount int
	_ = s.store.DB().QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE is_fake=false AND gender='boy'`).Scan(&boyCount)
	_ = s.store.DB().QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE is_fake=false AND gender='girl'`).Scan(&girlCount)
	// ---------------------------------

	// ---- چت‌های فعال ----
	var activeChats, fakeChats int
	_ = s.store.DB().QueryRow(ctx, `SELECT COUNT(*) FROM chats WHERE status='chatting'`).Scan(&activeChats)
	_ = s.store.DB().QueryRow(ctx, `SELECT COUNT(*) FROM chats WHERE status='chatting' AND is_fake=true`).Scan(&fakeChats)
	realChats := activeChats - fakeChats

	// تعداد کاربران منحصربه‌فرد در چت
	var chattingUsers int
	_ = s.store.DB().QueryRow(ctx, `
		SELECT COUNT(DISTINCT user_id)
		FROM (
			SELECT user_id_1 AS user_id FROM chats WHERE status='chatting'
			UNION
			SELECT user_id_2 FROM chats WHERE status='chatting'
		) AS u
	`).Scan(&chattingUsers)
	// ---------------------------------

	text := fmt.Sprintf(
		"📊 اطلاعات کاربران:\n"+
			"👤 تعداد کل: %d\n"+
			"👤 آنلاین: %d\n"+
			"👤 امروز عضو شدند: %d\n"+
			"👤 دیروز عضو شدند: %d\n"+
			"👤 بدون چت: %d\n"+
			"👤 بدون موجودی: %d\n"+
			"👤 دعوت شده: %d\n"+
			"🚫 بلاک: %d\n"+
			"👑 ادمین: %d\n"+
			"👦 پسر: %d\n"+
			"👧 دختر: %d\n"+
			"💬 چت‌های فعال: %d (واقعی: %d، فیک: %d)\n"+
			"👥 کاربران درون چت: %d\n\n"+
			"🚦 وضعیت Fleet: %s\n\n"+
			"📊 توزیع کاربران در ربات‌ها:\n%s\n"+
			"🟢 آنلاین هر ربات:\n%s",
		count(`SELECT count(*) FROM users WHERE is_fake = false`),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND last_activity > $1`, onlineThreshold),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND created_at>=$1`, today),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND created_at>=$1 AND created_at<$2`, yesterday, today),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND num_chats=0`),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND balance=0`),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND referral IS NOT NULL`),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND status='block'`),
		count(`SELECT count(*) FROM users WHERE is_fake = false AND status='admin'`),
		boyCount,
		girlCount,
		activeChats,
		realChats,
		fakeChats,
		chattingUsers,
		mode,
		botStats.String(),
		onlineStats.String(),
	)

	_, err = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": text})
	return err
}

func (s *Service) adminPaymentsList(ctx context.Context, c *UpdateContext) error {
	page := 1
	dataCurrent := "none"
	if c.Message == nil {
		dataCurrent = part(c.ExData, 1)
		page = parseInt(dataCurrent)
	}
	if page < 1 {
		page = 1
	}
	step := 30
	offset := (page - 1) * step
	rows, err := s.store.DB().Query(ctx, `SELECT id,status FROM payments ORDER BY id DESC LIMIT $1 OFFSET $2`, step, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	i := offset + 1
	var b strings.Builder
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			return err
		}
		b.WriteString(fmt.Sprintf("%d. شناسه: /p_%d (%s)\n", i, id, PaymentStatusText[status]))
		i++
	}
	if b.Len() == 0 {
		if dataCurrent == "none" {
			_, _ = s.sendMessage(ctx, c.UserID, "⚠️ تراکنشی یافت نشد.", 0, "")
		} else {
			_ = s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
		}
		return nil
	}
	var total int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&total)
	return s.listShow(ctx, c, "🔰 لیست ادمین\n\n"+b.String(), "paysListAdmin", total, page, step, nil, nil)
}

func (s *Service) adminUsersStep(ctx context.Context, c *UpdateContext) (bool, error) {
	if part(c.ExStep, 1) == "none" {
		switch c.Text {
		case "👥 ادمین":
			return true, s.adminSimpleUserList(ctx, c, "admin", "adminListAdmin", "🔰 لیست ادمین", "/delAdmin_")
		case "👥 بلاک":
			return true, s.adminSimpleUserList(ctx, c, "block", "blockListAdmin", "🔰 لیست کاربران:", "/unblock")
		case "👥 همه":
			return true, s.adminAllUsersList(ctx, c)
		case "👥 پیام همگانی":
			_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "users;m_all", "panel;users;none")
			_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "پیام خود را ارسال کنید:", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
			return true, err
		case "👥 فوروارد همگانی":
			_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "users;f_all", "panel;users;none")
			_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "پیام خود را فوروارد کنید:", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
			return true, err
		case "👥 سکه همگانی":
			_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "users;coin_all", "panel;users;none")
			_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "تعداد سکه را وارد کنید:", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
			return true, err
		}
	}
	switch part(c.ExStep, 1) {
	case "m_all":
		if c.Message == nil || c.Message.Text == "" {
			_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.ChatID, "text": "❌ فقط یک متن ارسال کنید"})
			return true, err
		}
		_ = s.store.UpdateUserStep(ctx, c.UserID, "users;none")
		return true, s.createBroadcast(ctx, c, "message", c.Text, 0)
	case "f_all":
		_ = s.store.UpdateUserStep(ctx, c.UserID, "users;none")
		return true, s.createBroadcast(ctx, c, "forward", "", c.MessageID)
	case "coin_all":
		coin := parseInt(c.Text)
		if coin < 1 {
			_, err := s.sendMessage(ctx, c.UserID, "❌ مشکلی پیش آمده، دوباره تلاش کنید.", 0, "")
			return true, err
		}
		_ = s.store.UpdateUserStep(ctx, c.UserID, "users;none")
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET balance=balance+$1`, coin)
		_, err := s.sendMessage(ctx, c.UserID, fmt.Sprintf("✅ تعداد %d سکه به موجودی همه کاربران اضافه شد؛ پیامی برای کاربران ارسال نشد.", coin), 0, "")
		return true, err
	}
	return false, nil
}

func (s *Service) createBroadcast(ctx context.Context, c *UpdateContext, fileType, text string, messageID int) error {
	var maxID int64
	var count int
	_ = s.store.DB().QueryRow(ctx, `SELECT coalesce(max(id),0) FROM users`).Scan(&maxID)
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count)
	resp, _ := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.ChatID, "text": "🔰 وضعیت: در حال آماده سازی..."})
	editID := 0
	if msg, ok := s.tg.SentMessage(resp); ok {
		editID = msg.MessageID
	}
	_, err := s.store.DB().Exec(ctx, `INSERT INTO cron (user_id,chat_id,type,file_type,text,message_id,max_send_id,message_id_edit,count_members) VALUES ($1,$2,'bot',$3,$4,$5,$6,$7,$8)`, c.UserID, c.UserID, fileType, text, messageID, maxID, editID, count)
	return err
}

func (s *Service) adminTariffStep(ctx context.Context, c *UpdateContext) error {
	switch part(c.ExStep, 1) {
	case "check":
		if c.Text == "افزودن تعرفه ➕" {
			_ = s.store.UpdateUserStep(ctx, c.UserID, "set_vip;day_vip")
			_, err := s.sendMessage(ctx, c.UserID, "تعداد سکه ها را وارد کنید.\n\n‼️ یک عدد اط 1 تا 10000:", 0, PanelButton)
			return err
		}
		var exists bool
		_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM amounts WHERE coin=$1)`, parseInt(c.Text)).Scan(&exists)
		if !exists {
			_, err := s.sendMessage(ctx, c.UserID, "❌ خطایی رخ داد.", 0, "")
			return err
		}
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM amounts WHERE coin=$1`, parseInt(c.Text))
		_, _ = s.sendMessage(ctx, c.UserID, "✅ تعرفه موردنظر با موفقیت حذف شد.", 0, "")
		return s.panel(ctx, c, "tariff")
	case "day_vip":
		coin := parseInt(c.Text)
		var exists bool
		_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM amounts WHERE coin=$1)`, coin).Scan(&exists)
		if exists {
			_, err := s.sendMessage(ctx, c.UserID, "❌ تعرفه مربوطه قبلا تعریف شده", 0, "")
			return err
		}
		if coin < 1 || coin > 10000 {
			_, err := s.sendMessage(ctx, c.UserID, "تعداد سکه ها را یک عدد از 1 تا 10000 وارد کنید:", 0, PanelButton)
			return err
		}
		_ = s.store.UpdateUserStep(ctx, c.UserID, fmt.Sprintf("set_vip;amount_vip;%d", coin))
		_, err := s.sendMessage(ctx, c.UserID, fmt.Sprintf("قیمت %d سکه را یک عدد از 1000 تا 10000000 وارد کنید:", coin), 0, PanelButton)
		return err
	case "amount_vip":
		amount := parseInt(c.Text)
		coin := parseInt(part(c.ExStep, 2))
		if amount < 1000 || amount > 10000000 {
			_, err := s.sendMessage(ctx, c.UserID, fmt.Sprintf("قیمت %d سکه را یک عدد بین 1000 تا 1000000 وارد کنید:", coin), 0, PanelButton)
			return err
		}
		_, _ = s.store.DB().Exec(ctx, `INSERT INTO amounts (amount,coin) VALUES ($1,$2) ON CONFLICT (coin) DO UPDATE SET amount=excluded.amount`, amount, coin)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "panel;none")
		_, _ = s.sendMessage(ctx, c.UserID, "✅ تعرفه جدید با موفقیت ثبت شد.", 0, "")
		return s.panel(ctx, c, "tariff")
	}
	return nil
}

func (s *Service) adminPaymentDetail(ctx context.Context, c *UpdateContext) error {
	id := parseInt64(part(c.ExText, 1))
	var p struct {
		ID, CreatedAt, UpdatedAt int64
		UserID, RefID, Status    string
		Amount                   int
	}
	err := s.store.DB().QueryRow(ctx, `SELECT id,user_id,coalesce(ref_id,''),status,amount,created_at,updated_at FROM payments WHERE id=$1`, id).Scan(&p.ID, &p.UserID, &p.RefID, &p.Status, &p.Amount, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		_, _ = s.sendMessage(ctx, c.UserID, "❌ تراکنش موردنظر یافت نشد.", 0, "")
		return nil
	}
	_, err = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": fmt.Sprintf("شناسه: <code>%d</code>\nپیگیری زرین پال: <code>%s</code>\nکاربر: <a href='tg://user?id=%s'>%s</a> (/d_%s)\nوضعیت: %s\nمبلغ: %s تومان\nایجاد: %s\nبروزرسانی: %s", p.ID, p.RefID, p.UserID, p.UserID, p.UserID, PaymentStatusText[p.Status], toPersian(formatNumber(p.Amount)), jdate(s.loc, "Y/m/d ساعت H:i:s", p.CreatedAt), jdate(s.loc, "Y/m/d ساعت H:i:s", p.UpdatedAt)), "parse_mode": "HTML", "reply_to_message_id": c.MessageID})
	return err
}

func (s *Service) adminSettingsStep(ctx context.Context, c *UpdateContext) error {
	if c.Text == "🗑 کانال کش" {
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "settings;ch_cache_id", "settings")
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "شناسه عددی کانال کش را بفرستید:\n\nاز این ربات برای بدست اوردن شناسه کانال استفاده کنید: @info_tel_bot", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
		return err
	}
	if part(c.ExStep, 1) == "ch_cache_id" {
		me, err := s.tg.GetMe(ctx)
		if err != nil {
			_, err := s.sendMessage(ctx, c.UserID, "❌ کانال مورد نظر شناسایی نشد!", 0, "")
			return err
		}
		status := s.isMember(ctx, c.Text, fmt.Sprint(me.ID))
		if status != "administrator" && status != "creator" {
			_, err := s.sendMessage(ctx, c.UserID, "❌ کانال مورد نظر شناسایی نشد!", 0, "")
			return err
		}
		_, _ = s.store.DB().Exec(ctx, `UPDATE admin SET ch_cache_id=$1 WHERE id=1`, c.Text)
		s.store.InvalidateAdmin(ctx)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "settings;none")
		_, _ = s.sendMessage(ctx, c.UserID, "✅ انجام شد.", 0, "")
		return s.panel(ctx, c, "settings")
	}
	if c.Text == "⚙️ اجباری 1" || c.Text == "⚙️ اجباری 2" || c.Text == "⚙️ اجباری 3" {
		channel := strings.TrimSpace(strings.TrimPrefix(c.Text, "⚙️ اجباری "))
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "settings;force_join;channel_"+channel, "settings")
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "یوزرنیم و نام کانال مانند نمونه در دو خط ارسال کنید:\n\nنام کانال\n@username", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
		return err
	}
	if part(c.ExStep, 1) == "force_join" {
		lines := strings.Split(c.Text, "\n")
		if len(lines) != 2 {
			_, err := s.sendMessage(ctx, c.UserID, "❌ کانال مورد نظر شناسایی نشد!", c.MessageID, "")
			return err
		}
		name := strings.TrimSpace(lines[0])
		channelInput := strings.TrimSpace(lines[1])
		if looksLikeChannelReference(name) && !looksLikeChannelReference(channelInput) {
			name, channelInput = channelInput, name
		}
		username := normalizeChannelUsername(channelInput)
		if username == "" || name == "" {
			_, err := s.sendMessage(ctx, c.UserID, "❌ نام یا یوزرنیم کانال معتبر نیست.", c.MessageID, "")
			return err
		}
		me, err := s.tg.GetMe(ctx)
		if err != nil {
			_, err := s.sendMessage(ctx, c.UserID, "❌ کانال شناسایی نشد یا ربات در آن ادمین نیست. ابتدا ربات را با دسترسی مشاهده اعضا ادمین کنید.", c.MessageID, "")
			return err
		}
		status := s.isMember(ctx, "@"+username, fmt.Sprint(me.ID))
		if status != "administrator" && status != "creator" {
			_, err := s.sendMessage(ctx, c.UserID, "❌ کانال شناسایی نشد یا ربات در آن ادمین نیست. ابتدا ربات را با دسترسی مشاهده اعضا ادمین کنید.", c.MessageID, "")
			return err
		}
		col := part(c.ExStep, 2)
		if col != "channel_1" && col != "channel_2" && col != "channel_3" {
			_, err := s.sendMessage(ctx, c.UserID, "❌ کانال مورد نظر شناسایی نشد!", c.MessageID, "")
			return err
		}
		_, _ = s.store.DB().Exec(ctx, `UPDATE admin SET `+col+`=$1, `+col+`_name=$2 WHERE id=1`, username, name)
		s.store.InvalidateAdmin(ctx)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "settings;none")
		_, _ = s.sendMessage(ctx, c.UserID, "✅ انجام شد.", 0, "")
		return s.panel(ctx, c, "settings")
	}
	if c.Text == "📭 پشتیبانی" || c.Text == "📭 گروه ادمین" {
		if c.Text == "📭 پشتیبانی" {
			_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "settings;support", "settings")
			_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "شناسه عددی گروه پشتیبانی را وارد کنید (مانند ‎-1001234567890). ربات باید در گروه عضو باشد:", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
			return err
		}
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "settings;admin_group", "settings")
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "شناسه عددی گروه ادمین‌ها را وارد کنید (مانند ‎-1001234567890). ربات باید در گروه عضو باشد:\n\n📌 این گروه برای دریافت پروفایل‌ها، فایل‌های چت، گزارشات و فیش‌های پرداخت در تاپیک‌های مختلف استفاده می‌شود.", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
		return err
	}
	if c.Text == "💳 تنظیم کارت‌به‌کارت" {
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "settings;card", "settings")
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "نام صاحب کارت و شماره کارت را در دو خط ارسال کنید:\n\nنام صاحب کارت\n6037991234567890", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(PanelButton)}}))})
		return err
	}
	if part(c.ExStep, 1) == "card" {
		lines := strings.Split(c.Text, "\n")
		if len(lines) != 2 {
			_, err := s.sendMessage(ctx, c.UserID, "❌ اطلاعات را دقیقاً در دو خط ارسال کنید.", c.MessageID, "")
			return err
		}
		holder := strings.TrimSpace(lines[0])
		number := strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(lines[1]))
		if len(number) != 16 || parseInt64(number) == 0 || holder == "" {
			_, err := s.sendMessage(ctx, c.UserID, "❌ شماره کارت باید ۱۶ رقم باشد.", c.MessageID, "")
			return err
		}
		_, _ = s.store.DB().Exec(ctx, `UPDATE admin SET card_number=$1,card_holder=$2 WHERE id=1`, number, holder)
		s.store.InvalidateAdmin(ctx)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "settings;none")
		_, _ = s.sendMessage(ctx, c.UserID, "✅ اطلاعات کارت ثبت شد.", 0, "")
		return s.panel(ctx, c, "settings")
	}
	if part(c.ExStep, 1) == "support" {
		groupID := strings.TrimSpace(c.Text)
		me, getMeErr := s.tg.GetMe(ctx)
		status := "left"
		if getMeErr == nil {
			status = s.isMember(ctx, groupID, fmt.Sprint(me.ID))
		}
		if getMeErr != nil || (status != "member" && status != "administrator" && status != "creator") {
			_, err := s.sendMessage(ctx, c.UserID, "❌ گروه شناسایی نشد یا ربات عضو گروه نیست.", c.MessageID, "")
			return err
		}
		_, _ = s.store.DB().Exec(ctx, `UPDATE admin SET support=$1 WHERE id=1`, groupID)
		s.store.InvalidateAdmin(ctx)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "settings;none")
		_, _ = s.sendMessage(ctx, c.UserID, "✅ انجام شد.", 0, "")
		return s.panel(ctx, c, "settings")
	}
	if part(c.ExStep, 1) == "admin_group" {
		groupID := strings.TrimSpace(c.Text)
		me, getMeErr := s.tg.GetMe(ctx)
		status := "left"
		if getMeErr == nil {
			status = s.isMember(ctx, groupID, fmt.Sprint(me.ID))
		}
		if getMeErr != nil || (status != "member" && status != "administrator" && status != "creator") {
			_, err := s.sendMessage(ctx, c.UserID, "❌ گروه شناسایی نشد یا ربات عضو گروه نیست.", c.MessageID, "")
			return err
		}
		_, _ = s.store.DB().Exec(ctx, `UPDATE admin SET admin_group_id=$1 WHERE id=1`, groupID)
		s.store.InvalidateAdmin(ctx)
		_ = s.store.UpdateUserStep(ctx, c.UserID, "settings;none")
		_, _ = s.sendMessage(ctx, c.UserID, "✅ گروه ادمین با موفقیت ذخیره شد.", 0, "")
		return s.panel(ctx, c, "settings")
	}
	return nil
}

func (s *Service) startTicketReply(ctx context.Context, c *UpdateContext) error {
	ticketID := parseInt64(part(c.ExData, 1))
	var status string
	if err := s.store.DB().QueryRow(ctx, `SELECT status FROM support_tickets WHERE id=$1`, ticketID).Scan(&status); err != nil {
		return s.answer(ctx, c, "تیکت پیدا نشد.")
	}
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, fmt.Sprintf("ticket_reply;%d", ticketID), "start")
	_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.ChatID, "message_thread_id": adminTopicSupport, "text": fmt.Sprintf("پاسخ تیکت را ارسال کنید:"), "reply_to_message_id": c.MessageID})
	return err
}

func (s *Service) submitTicketReply(ctx context.Context, c *UpdateContext) error {
	if c.Message == nil {
		return nil
	}
	ticketID := parseInt64(part(c.ExStep, 1))
	var userID string
	if err := s.store.DB().QueryRow(ctx, `SELECT user_id FROM support_tickets WHERE id=$1`, ticketID).Scan(&userID); err != nil {
		return err
	}

	trackingNum, err := s.ensureSupportTracking(ctx, ticketID)
	if err != nil {
		return err
	}

	responseText := fmt.Sprintf("📨 پاسخ پشتیبانی به تیکت #%d:\n", trackingNum)
	markup := telegram.JSON(replyMarkupInline([][]button{{callbackButton("بازم سوال دارم 📩", "support_inline")}}))
	resp, err := s.sendCombinedContent(ctx, userID, 0, responseText, c.Message, markup)
	if err != nil || !resp.Ok {
		return fmt.Errorf("send ticket reply failed")
	}
	_, _ = s.store.DB().Exec(ctx, `UPDATE support_tickets SET status='answered',answered_by=$2,updated_at=$3 WHERE id=$1`, ticketID, c.UserID, c.Now)
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
	_, err = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.ChatID, "message_thread_id": adminTopicSupport, "text": fmt.Sprintf("✅ پاسخ تیکت #%d ارسال شد.", trackingNum)})
	return err
}

func looksLikeChannelReference(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "@") || strings.Contains(value, "t.me/")
}

func normalizeChannelUsername(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "www.")
	value = strings.TrimPrefix(value, "t.me/")
	value = strings.TrimPrefix(value, "@")
	value = strings.Trim(value, "/")
	if strings.ContainsAny(value, " /?&") {
		return ""
	}
	return value
}

func (s *Service) adminSendMessageStep(ctx context.Context, c *UpdateContext) error {
	user, err := s.store.UserByUniqOrID(ctx, part(c.ExStep, 1))
	if err != nil {
		_, _ = s.sendMessage(ctx, c.UserID, "❌ کاربر دریافت کننده پیام یافت نشد!", c.MessageID, MainButton)
		return nil
	}
	var resp telegram.APIResponse
	if c.Message != nil && c.Message.Text != "" {
		resp, err = s.send(ctx, "sendMessage", map[string]any{"chat_id": user.UserID, "text": "📧️ پیام جدید از طرف پشتیبانی\n——————————————————\n" + c.Text, "parse_mode": "html", "disable_web_page_preview": true})
	} else if c.Message != nil && len(c.Message.Photo) > 0 {
		resp, err = s.send(ctx, "sendPhoto", map[string]any{"chat_id": user.UserID, "photo": c.Message.Photo[0].FileID, "caption": "📧️ پیام جدید از طرف پشتیبانی\n——————————————————\n" + c.Message.Caption, "parse_mode": "html", "disable_web_page_preview": true})
	} else if c.Message != nil {
		for _, typ := range []string{"video", "audio", "voice", "document"} {
			if id := fileIDByType(c.Message, typ); id != "" {
				method := "send" + strings.ToUpper(typ[:1]) + typ[1:]
				resp, err = s.send(ctx, method, map[string]any{"chat_id": user.UserID, typ: id, "caption": "📧️ پیام جدید از طرف پشتیبانی\n——————————————————\n" + c.Message.Caption, "parse_mode": "html", "disable_web_page_preview": true})
				break
			}
		}
	}
	if err != nil || !resp.Ok {
		_, _ = s.sendMessage(ctx, c.UserID, "❌ مشکلی در ارسال پیام رخ داد!", 0, "")
	} else {
		_, _ = s.sendMessage(ctx, c.UserID, "✅ پیام شما ارسال شد.", 0, "")
	}
	_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
	return nil
}

func (s *Service) adminStatusCommand(ctx context.Context, c *UpdateContext) error {
	user, err := s.store.UserByUniqOrID(ctx, part(c.ExText, 1))
	if err != nil {
		_, _ = s.sendMessage(ctx, c.UserID, "❌ یافت نشد!", 0, "")
		return nil
	}
	cmd := part(c.ExText, 0)
	switch cmd {
	case "/block":
		if user.Status == "block" {
			_, _ = s.sendMessage(ctx, c.UserID, "❌ کاربر هم اکنون در لیست بلاک است!", 0, "")
		} else {
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET status='block' WHERE user_id=$1`, user.UserID)
			_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": user.UserID, "text": "⛔️ حساب شما توسط مدیریت ربات مسدود شد. برای پیگیری می‌توانید از بخش پشتیبانی تیکت ثبت کنید."})
			_, _ = s.sendMessage(ctx, c.UserID, "انجام شد ✅", 0, "")
		}
	case "/unblock":
		if user.Status != "block" {
			_, _ = s.sendMessage(ctx, c.UserID, "❌ کاربر هم اکنون آزاد است!", 0, "")
		} else {
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET status='user' WHERE user_id=$1`, user.UserID)
			_, _ = s.sendMessage(ctx, c.UserID, "انجام شد ✅", 0, "")
		}
	case "/addAdmin":
		if user.Status == "admin" {
			_, _ = s.sendMessage(ctx, c.UserID, "❌ کاربر هم اکنون در لیست ادمین است!", 0, "")
		} else {
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET status='admin' WHERE user_id=$1`, user.UserID)
			_, _ = s.sendMessage(ctx, c.UserID, "انجام شد ✅", 0, "")
		}
	case "/delAdmin":
		if user.Status != "admin" {
			_, _ = s.sendMessage(ctx, c.UserID, "❌ کاربر هم اکنون آزاد است!", 0, "")
		} else {
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET status='user' WHERE user_id=$1`, user.UserID)
			_, _ = s.sendMessage(ctx, c.UserID, "انجام شد ✅", 0, "")
		}
	}
	return nil
}

func (s *Service) adminSimpleUserList(ctx context.Context, c *UpdateContext, status, markup, title, cmd string) error {
	page, dataCurrent := adminPage(c)
	step := 30
	offset := (page - 1) * step
	rows, err := s.store.DB().Query(ctx, `SELECT user_id FROM users WHERE status=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, status, step, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	var b strings.Builder
	i := offset + 1
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		b.WriteString(fmt.Sprintf("\n%d. <a href='tg://user?id=%s'>%s</a> (%s%s)", i, id, id, cmd, id))
		i++
	}
	if b.Len() == 0 {
		if dataCurrent == "none" {
			_, _ = s.sendMessage(ctx, c.UserID, "⚠️ کاربری یافت نشد.", 0, "")
		} else {
			_ = s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
		}
		return nil
	}
	var total int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM users WHERE status=$1`, status).Scan(&total)
	return s.listShow(ctx, c, title+"\n\n"+b.String(), markup, total, page, step, nil, nil)
}

func (s *Service) adminAllUsersList(ctx context.Context, c *UpdateContext) error {
	page, dataCurrent := adminPage(c)
	step := 30
	offset := (page - 1) * step
	rows, err := s.store.DB().Query(ctx, `SELECT user_id FROM users ORDER BY id DESC LIMIT $1 OFFSET $2`, step, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	var b strings.Builder
	i := offset + 1
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		b.WriteString(fmt.Sprintf("\n%d. <a href='tg://user?id=%s'>%s</a> (/d_%s)", i, id, id, id))
		i++
	}
	if b.Len() == 0 {
		if dataCurrent == "none" {
			_, _ = s.sendMessage(ctx, c.UserID, "⚠️ کاربری یافت نشد.", 0, "")
		} else {
			_ = s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
		}
		return nil
	}
	var total int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&total)
	return s.listShow(ctx, c, "🔰 لیست کاربران:\n"+b.String()+"\n‌", "usersListAdmin", total, page, step, nil, nil)
}

func adminPage(c *UpdateContext) (int, string) {
	dataCurrent := "none"
	if c.Message == nil {
		dataCurrent = part(c.ExData, 1)
	}
	page := 1
	if dataCurrent != "none" {
		page = parseInt(dataCurrent)
	}
	if page < 1 {
		page = 1
	}
	return page, dataCurrent
}

func startOfToday(loc *time.Location) time.Time {
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
}
