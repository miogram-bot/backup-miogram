package bot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"miogram/internal/storage"
	"miogram/internal/telegram"
)

func (s *Service) handleStatic(ctx context.Context, c *UpdateContext) (bool, error) {
	if c.Data == "stay_classroom" {
		return true, s.handleStayClassroom(ctx, c)
	}
	if strings.HasPrefix(c.Text, "/delete_messages_") || strings.HasPrefix(c.Text, "/delet_messages_") {
		return true, s.deleteEndedConversation(ctx, c)
	}
	if part(c.ExData, 0) == "card_payment" {
		return true, s.startCardPayment(ctx, c)
	}
	if part(c.ExData, 0) == "send_receipt" {
		return true, s.enterCardReceiptMode(ctx, c)
	}
	if part(c.ExStep, 0) == "card_receipt" {
		return true, s.submitCardReceipt(ctx, c)
	}
	if c.Text == SupportButton || c.Text == "/support" {
		return true, s.startSupportTicket(ctx, c)
	}
	if part(c.ExStep, 0) == "support" && part(c.ExStep, 1) == "new" {
		return true, s.submitSupportTicket(ctx, c)
	}
	switch c.Text {
	case "/on":
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "✅ جستجوی همسن برای شما فعال شد.\n" +
				"- برای غیر فعال سازی /off را بزنید\n\n" +
				"با قابلیت جستجوی همسن ، فقط افرادی که سن نزدیک به شما دارند جستجو خواهند شد.\n\n" +
				"<code>⚠️ جستجوی همسن باعث افزوده شدن فیلتر سن در جستجو می شود و می تواند باعث دیر پیدا شدن (و یا گاهی پیدا نشدن) مخاطب شما شود.</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
		})
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET same_age=true WHERE user_id=$1`, c.UserID)
		return true, err
	case "/off":
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "📴 جستجوی همسن برای شما غیرفعال شد.\n" +
				"- برای فعال سازی /on را بزنید\n\n" +
				"با قابلیت جستجوی همسن ، فقط افرادی که سن نزدیک به شما دارند جستجو خواهند شد.\n\n" +
				"<code>⚠️ جستجوی همسن باعث افزوده شدن فیلتر سن در جستجو می شود و می تواند باعث دیر پیدا شدن (و یا گاهی پیدا نشدن) مخاطب شما شود.</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
		})
		_, _ = s.store.DB().Exec(ctx, `UPDATE users SET same_age=false WHERE user_id=$1`, c.UserID)
		return true, err
	case "/deleteAllContacts":
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM friends WHERE user_id=$1`, c.UserID)
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "✅ تمامی مخاطبان شما با موفقیت حذف شدند.", "reply_to_message_id": c.MessageID})
		return true, err
	case "/deleteAllBlocks":
		_, _ = s.store.DB().Exec(ctx, `DELETE FROM blocked WHERE user_id=$1`, c.UserID)
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "✅ تمامی بلاک شده ها آزاد شدند.", "reply_to_message_id": c.MessageID})
		return true, err
	}

	if c.Text == "💰سکه" || c.Text == "/credit" {
		kb, err := s.coinKeyboard(ctx)
		if err != nil {
			return true, err
		}
		_, err = s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": fmt.Sprintf("💰سکه فعلی شما : %d\nـــــــــــــــــــــــــــــــ\n❓روش های بدست آوردن سکه چیست؟\n\nبرای افزایش سکه به صورت رایگان بنر لینک⚡️ مخصوص خودت (/link) رو برای دوستات  بفرست و %d سکه دریافت کن\n\n- برای اطلاعات بیشتر راهنمای سکه رو بخون (/help_credit)\n\n2️⃣خرید سکه بصورت آنلاین :\n\n<code>برای خرید سکه یکی از تعرفه های زیر را انتخاب نمایید👇</code>",
				c.User.Balance, c.Admin.CoinPerInvite),
			"reply_to_message_id": c.MessageID,
			"parse_mode":          "HTML",
			"reply_markup":        telegram.JSON(replyMarkupInline(kb)),
		})
		return true, err
	}
	if part(c.ExData, 0) == "buy_coin" {
		return true, s.buyCoin(ctx, c)
	}
	if c.Text == "🤔راهنما" || c.Text == "/help" {
		return true, s.sendHelpMenu(ctx, c)
	}
	if c.Data == "support_inline" {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return true, s.startSupportTicket(ctx, c)
	}
	if part(c.ExText, 0) == "/help" {
		if handled, err := s.helpTopic(ctx, c, part(c.ExText, 1)); handled || err != nil {
			return handled, err
		}
	}
	if c.Text == "/ghavanin" || c.Text == "/help_terms" {
		text := "🚦🚧 قوانين استفاده از ربات " + s.cfg.BotName + " 🚧🚦\n\n" +
			"موارد زیر باعث مسدود شدن دائمی کاربر خواهد شد.\n\n" +
			"1️⃣ تبلیغات سایت ها ربات ها و کانال ها\n\n" +
			"2️⃣ ارسال هرگونه محتوای غیر اخلاقی\n\n" +
			"3️⃣ ایجاد مزاحمت برای کاربران\n\n" +
			"4️⃣ پخش شماره موبایل یا اطلاعات شخصی دیگران\n\n" +
			"5️⃣ محتوای غیر اخلاقی و یا توهین آمیز در پروفایل " + s.cfg.BotName + "\n\n" +
			"6️⃣ ثبت جنسیت اشتباه در پروفایل\n\n" +
			"7️⃣ تهدید و جا زدن خود بعنوان مدیر ربات یا پلیس فتا !\n\n" +
			"برای گزارش عدم رعایت قوانین می‌توانید گزینه 《🚫 گزارش کاربر》 را در پروفایل کاربر لمس کنید.\n\n" +
			"👈 درصورت تأیید گزارش، ۵ سکه هدیه دریافت می‌کنید.\n\nراهنما: /help"
		return true, s.sendHelpImage(ctx, c, "ghavanin.jpg", text, "")
	}
	if c.Text == "🚸 معرفی به دوستان (سکه رایگان)" || c.Text == "/link" || c.Data == "invite" {
		return true, s.invite(ctx, c)
	}
	return false, nil
}

// sendHelpMenu sends the main help menu with all help topics
func (s *Service) sendHelpMenu(ctx context.Context, c *UpdateContext) error {
	text := "🔹راهنمای استفاده از ربات:\n\n" +
		"<code>من اینجام که کمکت کنم! برای دریافت راهنمایی در مورد هر موضوع، کافیه دستور آبی رنگی که مقابل اون سوال هست رو لمس کنی:</code>\n\n" +
		"⁉️ چگونه بصورت ناشناس چت کنم؟ /help_chat\n\n" +
		"⁉️ سکه یا امتیاز چیست؟ /help_credit\n\n" +
		"⁉️ چگونه افراد نزدیکمو پیدا کنم؟ /help_gps\n\n" +
		"⁉️ پروفایل چیست؟ /help_profile\n\n" +
		"⁉️ چگونه درخواست چت بفرستم؟ /help_sendchat\n\n" +
		"⁉️ پیام دایرکت چیست؟ /help_direct\n\n" +
		"⁉️ چگونه با 'میان بر' ها کار کنم؟ /help_shortcuts\n\n" +
		"⁉️ اطلاع رسانی آنلاین شدن مخاطب /help_onw\n\n" +
		"⁉️ اطلاع رسانی اتمام چت مخاطب /help_chw\n\n" +
		"⁉️ لیست مخاطبین چیست ؟ /help_contacts\n\n" +
		"⁉️ چگونه بصورت پیشرفته بین کاربران جستجو کنم ؟ /help_search\n\n" +
		"⁉️ آموزش حذف پیام در چت /help_deleteMessage\n\n" +
		"⚖️ قوانین استفاده از ربات /ghavanin"

	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":             c.UserID,
		"text":                text,
		"reply_to_message_id": c.MessageID,
		"parse_mode":          "HTML",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{{
			callbackButton("📨 پشتیبانی", "support_inline"),
		}})),
	})
	return err
}

func (s *Service) deleteEndedConversation(ctx context.Context, c *UpdateContext) error {
	uniq := strings.TrimPrefix(c.Text, "/delete_messages_")
	uniq = strings.TrimPrefix(uniq, "/delet_messages_")
	if uniq == "" {
		return nil
	}
	other, err := s.store.UserByUniqOrID(ctx, uniq)
	if err != nil {
		_, sendErr := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "⚠️ گفتگوی مورد نظر پیدا نشد."})
		return sendErr
	}
	var chatID int64
	err = s.store.DB().QueryRow(ctx, `SELECT id FROM chats WHERE status='end' AND ended_at+$3>=$4 AND ((user_id_1=$1 AND user_id_2=$2) OR (user_id_1=$2 AND user_id_2=$1)) ORDER BY ended_at DESC LIMIT 1`, c.UserID, other.UserID, int64(1800), c.Now).Scan(&chatID)
	if err != nil {
		_, sendErr := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "⚠️ مهلت ۳۰ دقیقه‌ای حذف پیام‌ها تمام شده یا گفتگو پیدا نشد."})
		return sendErr
	}
	rows, err := s.store.DB().Query(ctx, `SELECT user_id_1,message_id_1,user_id_2,message_id_2 FROM chatmsgs WHERE chat_id=$1`, chatID)
	if err != nil {
		return err
	}
	type mappedMessage struct {
		fromID, toID           string
		fromMessage, toMessage int
	}
	items := []mappedMessage{}
	for rows.Next() {
		var item mappedMessage
		if err := rows.Scan(&item.fromID, &item.fromMessage, &item.toID, &item.toMessage); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		s.deleteMessage(ctx, item.fromID, item.fromMessage)
		if item.toMessage > 0 && !strings.HasPrefix(item.toID, "fake:") {
			s.deleteMessage(ctx, item.toID, item.toMessage)
		}
	}
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text":    fmt.Sprintf("✅ حذف %d پیام ثبت‌شده از گفتگوی هر دو طرف انجام شد.", len(items)),
	})
	return err
}

func (s *Service) startSupportTicket(ctx context.Context, c *UpdateContext) error {
	if c.Admin.AdminGroupID == "" && c.Admin.Support == "" {
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "پشتیبانی هنوز توسط مدیریت تنظیم نشده است."})
		return err
	}
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "support;new", "start")
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "📨 پیام، تصویر یا فایل خود را برای پشتیبانی ارسال کنید. پاسخ تیم پشتیبانی در همین ربات برای شما می‌آید.",
		"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(MainButton)}})),
	})
	return err
}

func (s *Service) submitSupportTicket(ctx context.Context, c *UpdateContext) error {
	if c.Message == nil {
		return nil
	}
	var ticketID, trackingNum int64
	for attempt := 0; attempt < 20; attempt++ {
		trackingNum = generateRandom5Digit()
		err := s.store.DB().QueryRow(ctx, `INSERT INTO support_tickets (user_id,status,created_at,updated_at,tracking_number) VALUES ($1,'open',$2,$2,$3) ON CONFLICT DO NOTHING RETURNING id`, c.UserID, c.Now, trackingNum).Scan(&ticketID)
		if err == nil {
			break
		}
		if err != pgx.ErrNoRows {
			return err
		}
	}
	if ticketID == 0 {
		return fmt.Errorf("could not allocate support tracking number")
	}
	userName := c.User.Name
	if userName == "" {
		userName = "کاربر"
	}
	groupID := c.Admin.AdminGroupID
	if groupID == "" {
		groupID = c.Admin.Support
	}
	header := fmt.Sprintf("🎫 تیکت #%d\n👨‍💼 کاربر: %s\n🆔 شناسه پروفایل: /user_%s\n📇 نام: %s\n\n🏷 متن سوال:\n", trackingNum, c.UserID, c.User.UniqID, userName)
	markup := telegram.JSON(replyMarkupInline([][]button{{callbackButton("پاسخ به سوال کاربر 📝", fmt.Sprintf("ticket_reply;%d", ticketID))}}))
	resp, err := s.sendCombinedContent(ctx, groupID, adminTopicSupport, header, c.Message, markup)
	if err != nil || !resp.Ok {
		if err != nil {
			return err
		}
		return fmt.Errorf("send support ticket: %s", resp.Description)
	}
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         fmt.Sprintf("✅ تیکت با کد پیگیری #%d ثبت شد. پس از بررسی، پاسخ برای شما ارسال می‌شود.", trackingNum),
		"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	return err
}

func (s *Service) sendCombinedContent(ctx context.Context, chatID string, threadID int, prefix string, message *telegram.Message, markup string) (telegram.APIResponse, error) {
	params := map[string]any{"chat_id": chatID, "reply_markup": markup}
	if threadID > 0 {
		params["message_thread_id"] = threadID
	}
	method := "sendMessage"
	content := message.Text
	if content == "" {
		content = message.Caption
	}
	switch {
	case message.Text != "":
		params["text"] = truncateRunes(prefix+content, 4000)
	case len(message.Photo) > 0:
		method, params["photo"], params["caption"] = "sendPhoto", photoID(message), truncateRunes(prefix+content, 1000)
	case message.Video != nil:
		method, params["video"], params["caption"] = "sendVideo", message.Video.FileID, truncateRunes(prefix+content, 1000)
	case message.Audio != nil:
		method, params["audio"], params["caption"] = "sendAudio", message.Audio.FileID, truncateRunes(prefix+content, 1000)
	case message.Voice != nil:
		method, params["voice"], params["caption"] = "sendVoice", message.Voice.FileID, truncateRunes(prefix+content, 1000)
	case message.Animation != nil:
		method, params["animation"], params["caption"] = "sendAnimation", message.Animation.FileID, truncateRunes(prefix+content, 1000)
	case message.Document != nil:
		method, params["document"], params["caption"] = "sendDocument", message.Document.FileID, truncateRunes(prefix+content, 1000)
	default:
		return telegram.APIResponse{}, fmt.Errorf("unsupported support message type")
	}
	return s.send(ctx, method, params)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func (s *Service) coinKeyboard(ctx context.Context) ([][]button, error) {
	rows, err := s.store.DB().Query(ctx, `SELECT id,amount,coin FROM amounts ORDER BY amount ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kb := [][]button{{callbackButton("🚸 معرفی به دوستان (سکه رایگان)", "invite")}}
	for rows.Next() {
		var a storage.Amount
		if err := rows.Scan(&a.ID, &a.Amount, &a.Coin); err != nil {
			return nil, err
		}
		kb = append(kb, []button{callbackButton(fmt.Sprintf("%d سکه %s تومان", a.Coin, formatNumber(a.Amount)), fmt.Sprintf("buy_coin;%d", a.ID))})
	}
	return kb, rows.Err()
}

func (s *Service) buyCoin(ctx context.Context, c *UpdateContext) error {
	_ = s.ack(ctx, c)
	id := parseInt64(part(c.ExData, 1))
	var amount storage.Amount
	err := s.store.DB().QueryRow(ctx, `SELECT id,amount,coin FROM amounts WHERE id=$1`, id).Scan(&amount.ID, &amount.Amount, &amount.Coin)
	if err != nil {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	var payment storage.Payment
	err = s.store.DB().QueryRow(ctx, `
		SELECT id,user_id,coins,amount,coalesce(authority,''),coalesce(ref_id,''),status,created_at,updated_at,coalesce(uniq_id,'')
		FROM payments
		WHERE user_id=$1 AND coins=$2 AND ($3-updated_at)<750 AND status IN ('first_level','move_gateway')
		ORDER BY id DESC LIMIT 1`, c.UserID, amount.Coin, c.Now).
		Scan(&payment.ID, &payment.UserID, &payment.Coins, &payment.Amount, &payment.Authority, &payment.RefID, &payment.Status, &payment.CreatedAt, &payment.UpdatedAt, &payment.UniqID)
	if err == pgx.ErrNoRows {
		uniq, err := s.store.RandomUniq(ctx, "payments", 10)
		if err != nil {
			return err
		}
		err = s.store.DB().QueryRow(ctx, `
			INSERT INTO payments (user_id,coins,amount,status,created_at,updated_at,uniq_id)
			VALUES ($1,$2,$3,'first_level',$4,$4,$5)
			RETURNING id,user_id,coins,amount,coalesce(authority,''),coalesce(ref_id,''),status,created_at,updated_at,coalesce(uniq_id,'')`,
			c.UserID, amount.Coin, amount.Amount, c.Now, uniq).
			Scan(&payment.ID, &payment.UserID, &payment.Coins, &payment.Amount, &payment.Authority, &payment.RefID, &payment.Status, &payment.CreatedAt, &payment.UpdatedAt, &payment.UniqID)
	}
	if err != nil {
		return err
	}
	if _, err := s.ensurePaymentTracking(ctx, payment.ID); err != nil {
		return err
	}

	hasGateway := s.cfg.MerchantID != "" && s.cfg.PaymentGateway != ""
	hasCardPayment := c.Admin.CardNumber != ""

	// Show gateway image and warning only if gateway is available
	if hasGateway {
		_, _ = s.send(ctx, "sendPhoto", map[string]any{
			"chat_id": c.UserID,
			"photo":   s.helpImage(ctx, "gateway.jpg"),
			"caption": "⚠️ دقت کنید !\nحتما پس از پرداخت گزینه «تکمیل فراید پرداخت» را لمس کنید تا سکه را دریافت کنید.\n\nدرصورت عدم ورود به درگاه پرداخت ، فیلترشکن خود را خاموش کنید.",
		})
	}

	// Build payment buttons
	buttons := [][]button{}
	if hasCardPayment {
		buttons = append(buttons, []button{callbackButton("💳 پرداخت کارت‌به‌کارت", fmt.Sprintf("card_payment;%d", payment.ID))})
	}
	if hasGateway {
		buttons = append(buttons, []button{urlButton("🏦 پرداخت آنلاین", s.paymentURL(payment.ID))})
	}

	// Text based on available payment methods
	paymentText := ""
	if hasGateway {
		paymentText = fmt.Sprintf("▪️ پرداخت از طریق درگاه بانکی شتابی بصورت کاملا امن انجام میگیرد.\n\n⚠️ هنگام پرداخت حتما باید فیلترشکن خود را خاموش کنید ❗️\n\nلینک خرید %d سکه به مبلغ %s تومان برای شما ساخته شد 👇",
			amount.Coin, formatNumber(amount.Amount))
	} else if hasCardPayment {
		paymentText = fmt.Sprintf("💳 خرید %d سکه به مبلغ %s تومان\n\n—— روش پرداخت ———\n\n✅ کارت به کارت:\nپس از انتخاب گزینه «پرداخت کارت‌به‌کارت»، اطلاعات کارت برای شما ارسال می‌شود.\nپس از واریز، روی «ارسال فیش» بزنید و تصویر فیش را آپلود کنید.\nپس از تأیید ادمین، سکه به حساب شما افزوده می‌شود.",
			amount.Coin, formatNumber(amount.Amount))
	} else {
		paymentText = fmt.Sprintf("خرید %d سکه به مبلغ %s تومان", amount.Coin, formatNumber(amount.Amount))
	}

	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         paymentText,
		"parse_mode":   "HTML",
		"reply_markup": telegram.JSON(replyMarkupInline(buttons)),
	})
	return err
}

func (s *Service) startCardPayment(ctx context.Context, c *UpdateContext) error {
	if c.Admin.CardNumber == "" {
		return s.answer(ctx, c, "پرداخت کارت‌به‌کارت هنوز تنظیم نشده است.")
	}
	paymentID := parseInt64(part(c.ExData, 1))
	var amount, coins, trackingNumber int
	var status string
	err := s.store.DB().QueryRow(ctx, `SELECT amount,coins,status,tracking_number FROM payments WHERE id=$1 AND user_id=$2`, paymentID, c.UserID).Scan(&amount, &coins, &status, &trackingNumber)
	if err != nil {
		return s.answer(ctx, c, "این پرداخت معتبر نیست.")
	}
	if status != "first_level" && status != "move_gateway" && status != "card_receipt" {
		return s.answer(ctx, c, "این پرداخت قبلاً انجام شده یا منقضی شده است.")
	}
	ensuredTracking, err := s.ensurePaymentTracking(ctx, paymentID)
	if err != nil {
		return err
	}
	trackingNumber = int(ensuredTracking)

	// Update status to card_receipt but DON'T enter receipt mode yet
	_, _ = s.store.DB().Exec(ctx, `UPDATE payments SET status='card_receipt',updated_at=$3 WHERE id=$1 AND user_id=$2`, paymentID, c.UserID, c.Now)

	amountRial := strconv.Itoa(amount * 10)
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": fmt.Sprintf("💳 پرداخت کارت‌به‌کارت\n\n🔢 کد پیگیری: <code>%d</code>\n\n—— اطلاعات واریز ———\n\n💳 شماره کارت:\n<code>%s</code>\n\n👤 به نام:\n%s\n\n💰 مبلغ: %s تومان (%s ریال)\n💎 تعداد سکه: %d\n\n—— نحوه پرداخت ———\n\n1️⃣ مبلغ را به شماره کارت بالا واریز کنید.\n2️⃣ پس از واریز، روی دکمه «ارسال فیش» بزنید.\n3️⃣ تصویر فیش واریزی را ارسال کنید.\n4️⃣ پس از تأیید ادمین، سکه به حساب شما اضافه می‌شود.",
			trackingNumber, c.Admin.CardNumber, c.Admin.CardHolder, formatNumber(amount), formatNumber(amount*10), coins),
		"parse_mode": "HTML",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{copyButton("📋 کپی شماره کارت", c.Admin.CardNumber)},
			{copyButton("📋 کپی مبلغ به ریال", amountRial)},
			{callbackButton("📤 ارسال فیش", fmt.Sprintf("send_receipt;%d", paymentID))},
		})),
	})
	return err
}

func (s *Service) enterCardReceiptMode(ctx context.Context, c *UpdateContext) error {
	paymentID := parseInt64(part(c.ExData, 1))
	var status string
	err := s.store.DB().QueryRow(ctx, `SELECT status FROM payments WHERE id=$1 AND user_id=$2`, paymentID, c.UserID).Scan(&status)
	if err != nil || status != "card_receipt" {
		return s.answer(ctx, c, "این پرداخت معتبر نیست یا قبلاً فیش آن ارسال شده است.")
	}
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, fmt.Sprintf("card_receipt;%d", paymentID), "start")
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text":    "📤 لطفاً تصویر فیش واریزی را ارسال کنید.\n\nبرای لغو، دکمه «لغو ارسال» را بزنید.",
		"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{
			{textButton("لغو ارسال")},
		})),
	})
	return err
}

func (s *Service) submitCardReceipt(ctx context.Context, c *UpdateContext) error {
	paymentID := parseInt64(part(c.ExStep, 1))

	// Handle cancellation
	if c.Text == "لغو ارسال" {
		_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         "✅ ارسال فیش لغو شد. می‌توانید بعداً اقدام کنید.",
			"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
		})
		return err
	}

	fileID := photoID(c.Message)
	if fileID == "" {
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "❌ لطفاً فیش را به‌صورت تصویر ارسال کنید."})
		return err
	}
	ensuredTracking, err := s.ensurePaymentTracking(ctx, paymentID)
	if err != nil {
		return err
	}
	var amount, coins, trackingNumber int
	err = s.store.DB().QueryRow(ctx, `UPDATE payments SET status='card_review',receipt_file_id=$2,updated_at=$3 WHERE id=$1 AND user_id=$4 AND status='card_receipt' RETURNING amount,coins,tracking_number`, paymentID, fileID, c.Now, c.UserID).Scan(&amount, &coins, &trackingNumber)
	if err != nil {
		return err
	}
	trackingNumber = int(ensuredTracking)

	buyerUsername := getUserUsername(ctx, s, c.UserID)

	adminCaption := fmt.Sprintf("🔢 کد پیگیری: %d\n🤵‍♂️ خریدار :\n%s\n/user_%s\n/user_%s\n💰 مبلغ: %s تومان\n💎 تعداد سکه: %d",
		trackingNumber, buyerUsername, c.UserID, c.User.UniqID, formatNumber(amount), coins)

	success := false
	adminGroupID := s.adminGroupID(ctx)
	if adminGroupID != "" {
		resp, sendErr := s.send(ctx, "sendPhoto", map[string]any{
			"chat_id":           adminGroupID,
			"message_thread_id": adminTopicPaymentReceipt,
			"photo":             fileID,
			"caption":           adminCaption,
			"parse_mode":        "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("✅ تایید رسید پرداخت", fmt.Sprintf("card_review;approve;%d", paymentID))},
				{callbackButton("❌ رد کردن رسید", fmt.Sprintf("card_review;reject;%d", paymentID))},
			})),
		})
		if sendErr == nil && resp.Ok {
			success = true
			if msg, ok := s.tg.SentMessage(resp); ok {
				_, _ = s.store.DB().Exec(ctx, `UPDATE payments SET message_id_admin=$2 WHERE id=$1`, paymentID, msg.MessageID)
			}
		}
	}

	if !success {
		// Fallback to support group
		reviewChatID := c.Admin.Support
		if reviewChatID == "" {
			reviewChatID = s.cfg.AdminID
		}
		fallbackParams := map[string]any{
			"chat_id":    reviewChatID,
			"photo":      fileID,
			"caption":    adminCaption,
			"parse_mode": "HTML",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("✅ تایید رسید پرداخت", fmt.Sprintf("card_review;approve;%d", paymentID))},
				{callbackButton("❌ رد کردن رسید", fmt.Sprintf("card_review;reject;%d", paymentID))},
			})),
		}
		if reviewChatID == c.Admin.AdminGroupID {
			fallbackParams["message_thread_id"] = adminTopicPaymentReceipt
		}
		_, _ = s.send(ctx, "sendPhoto", fallbackParams)
	}

	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
	_, err = s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         fmt.Sprintf("✅ فیش با کد پیگیری #%d دریافت شد و پس از بررسی نتیجه اعلام می‌شود.", trackingNumber),
		"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	return err
}

func (s *Service) ensurePaymentTracking(ctx context.Context, paymentID int64) (int64, error) {
	var current int64
	if err := s.store.DB().QueryRow(ctx, `SELECT tracking_number FROM payments WHERE id=$1`, paymentID).Scan(&current); err != nil {
		return 0, err
	}
	if current >= 10000 && current <= 99999 {
		return current, nil
	}
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		candidate := generateRandom5Digit()
		tag, err := s.store.DB().Exec(ctx, `UPDATE payments SET tracking_number=$2 WHERE id=$1 AND (tracking_number<10000 OR tracking_number>99999) AND NOT EXISTS (SELECT 1 FROM payments WHERE tracking_number=$2)`, paymentID, candidate)
		if err != nil {
			lastErr = err
			continue
		}
		if tag.RowsAffected() == 1 {
			return candidate, nil
		}
		if err := s.store.DB().QueryRow(ctx, `SELECT tracking_number FROM payments WHERE id=$1`, paymentID).Scan(&current); err == nil && current >= 10000 && current <= 99999 {
			return current, nil
		}
	}
	if lastErr != nil {
		return 0, fmt.Errorf("allocate payment tracking number: %w", lastErr)
	}
	return 0, fmt.Errorf("could not allocate payment tracking number")
}

func (s *Service) ensureSupportTracking(ctx context.Context, ticketID int64) (int64, error) {
	var current int64
	if err := s.store.DB().QueryRow(ctx, `SELECT tracking_number FROM support_tickets WHERE id=$1`, ticketID).Scan(&current); err != nil {
		return 0, err
	}
	if current >= 10000 && current <= 99999 {
		return current, nil
	}
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		candidate := generateRandom5Digit()
		tag, err := s.store.DB().Exec(ctx, `UPDATE support_tickets SET tracking_number=$2 WHERE id=$1 AND (tracking_number<10000 OR tracking_number>99999) AND NOT EXISTS (SELECT 1 FROM support_tickets WHERE tracking_number=$2)`, ticketID, candidate)
		if err != nil {
			lastErr = err
			continue
		}
		if tag.RowsAffected() == 1 {
			return candidate, nil
		}
		if err := s.store.DB().QueryRow(ctx, `SELECT tracking_number FROM support_tickets WHERE id=$1`, ticketID).Scan(&current); err == nil && current >= 10000 && current <= 99999 {
			return current, nil
		}
	}
	if lastErr != nil {
		return 0, fmt.Errorf("allocate support tracking number: %w", lastErr)
	}
	return 0, fmt.Errorf("could not allocate support tracking number")
}

// sendHelpImage sends a help image with caption as a single message
func (s *Service) sendHelpImage(ctx context.Context, c *UpdateContext, name, caption, markup string) error {
	// Always send as photo with caption (single message)
	params := map[string]any{
		"chat_id":             c.UserID,
		"photo":               s.helpImage(ctx, name),
		"caption":             caption,
		"reply_to_message_id": c.MessageID,
		"parse_mode":          "HTML",
	}
	if markup != "" {
		params["reply_markup"] = markup
	}
	resp, err := s.send(ctx, "sendPhoto", params)
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("send help image %s: %s", name, resp.Description)
	}
	if msg, ok := s.tg.SentMessage(resp); ok && len(msg.Photo) > 0 {
		fileID := msg.Photo[len(msg.Photo)-1].FileID
		s.cacheHelpImage(name, fileID)
		_, _ = s.store.DB().Exec(ctx, `INSERT INTO telegram_assets (name,file_id,updated_at) VALUES ($1,$2,$3) ON CONFLICT (name) DO UPDATE SET file_id=excluded.file_id,updated_at=excluded.updated_at`, name, fileID, c.Now)
	}
	return nil
}

// helpTopic handles individual help topics
func (s *Service) helpTopic(ctx context.Context, c *UpdateContext, topic string) (bool, error) {
	text := ""
	markup := ""
	imageName := map[string]string{
		"chat": "help_chat.jpg", "credit": "help_credit.jpg", "gps": "help_gps.jpg",
		"profile": "help_profile.jpg", "sendchat": "help_sendchat.jpg", "direct": "help_direct.jpg",
		"shortcuts": "help_shortcuts.jpg", "onw": "help_onw.jpg",
	}[topic]
	switch topic {
	case "chat":
		text = "🔹برای چت میتونی یکی از راه های زیر رو انتخاب کنی:\n\n<code>⚜️ جستجوی تصادفی رو از بخش شروع چت انتخاب کنی و به صورت تصادفی به یکی وصل بشی! (بدون نیاز به سکه)</code>\n\n<code>⚜️ جستجو بر اساس جنسیت مورد نظر رو از بخش شروع چت انتخاب کنی و به یکی وصل بشی (نیاز به 2 سکه)</code>\n\n<code>⚜️ جستجو براساس شهر مورد نظر رو از بخش شروع چت انتخاب کنی و به انتخاب خودت به یکی وصل بشی</code>\n\n<code>⚜️ جستجو براساس موضوع مورد نظر ، از بخش شروع چت گزینه چت روم اختصاصی رو انتخاب کنی و براساس موضوع موردنظر و به انتخاب خودت به یکی وصل بشی</code>\n\n⚠️ اطلاعات شخصی شما مثل موقعیت GPS یا اسم شما در تلگرام یا عکس پروفایل و.. کاملا مخفی هست و فقط اطلاعاتی که تو ربات ثبت میکنید مانند شهر و عکس(توی ربات) برای کاربرای ربات قابل مشاهده هست."
	case "credit":
		text = "🔹 سکه یا امتیاز چیست؟\n\nبا سکه می‌توانید:\n" +
			"<code>• پیام دایرکت بفرستید (۱ سکه)\n• درخواست چت بفرستید (۲ سکه)\n• جستجوی دختر یا پسر انجام دهید (۲ سکه)\n• اعلان آنلاین‌شدن یا پایان چت را فعال کنید (۱ سکه)</code>\n\n" +
			"📢 سکه فقط وقتی کم می‌شود که درخواست با موفقیت انجام شود.\n\n" +
			"❓ راه‌های دریافت سکه:\n1️⃣ معرفی دوستان با لینک اختصاصی /link\n2️⃣ خرید آنلاین سکه\n3️⃣ گزارش کاربر متخلف؛ پس از بررسی و تأیید گزارش، ۵ سکه هدیه می‌گیرید.\n\n" +
			"برای گزارش، گزینه «🚫 گزارش کاربر» را در پروفایل او لمس کنید.\n\n" +
			"با معرفی هر نفر مجموعاً ۲۰ سکه می‌گیرید: ۷ سکه هنگام ورود، ۸ سکه بعد از تکمیل پروفایل و ۵ سکه پس از معرفی کاربر جدید توسط او."
	case "gps":
		text = "🔹چگونه افراد نزدیکمو پیدا کنم؟\n\nبرای دیدن لیست افراد نزدیکت فقط کافیه 《📍پیدا کردن افراد نزدیک با GPS》 رو لمس کنی.\n\n<code>- جستجوی افراد نزدیک کاملا رایگان هست (بدون نیاز به سکه)</code>\n\n\nبرای مشاهده کردن و یا چت کردن با افراد نزدیکت کافیه توی لیست روی آیدی شون بزنی تا پروفایلشونو ببینی.\n\n\n📢 توجه: امکان مشاهده موقعیت کاربران وجود ندارد و فقط فاصله آنها نمایش داده می شود."
		markup = telegram.JSON(replyMarkupInline([][]button{{callbackButton("📍 ثبت موقعیت GPS", "profile;gps")}}))
	case "profile":
		text = "🔹پروفایل چیست؟\n\n⚜️برای دیدن پروفایل خودت کافیه 《👤 پروفایل》 رو لمس کنی.\n⚜️برای دیدن پروفایل کسی که باهاش چت میکنی کافیه ایدی کاربری که باهاش چت میکنی رو لمس کنی.\n\n⚜️برای دیدن پروفایل هرکاربر کافیه روی آیدیش تو ربات بزنی.\n\n\n<code>📢  آیدی چیست؟ کد اختصاصی هر کاربر که با زدن آن پروفایل کاربر نمایش داده میشود و به صورت /user_ است.</code>\n\n- پروفایل هر کاربر شامل اطلاعاتی که تو ربات ثبت کرده (نام،سن،جنسیت،شهر،عکس) و تاریخ حضورش تو ربات و فاصلش با شمامیشه.\n\nبرای ارسال پیام دایرکت یا درخواست چت برای هر کاربر ابتدا باید پروفایلش رو مشاهده کنی و سپس دکمه ارسال پیام دایرکت یا درخواست چت رو بزنی."
	case "sendchat":
		text = "🔹چگونه درخواست چت بفرستم؟\n\nبرای ارسال درخواست چت به کاربران باید گزینه 《💬 درخواست چت》 رو در پروفایل کاربر لمس کنی.\n\n\n<code>- با ارسال درخواست چت تا وقتی که تایید نشده ازتون سکه ای کم نمیشه،درخواست چت وصل شد 2 سکه ازتون کم میشه. </code>\n\n 📢 توجه: امکان ارسال درخواست چت فقط برای کاربرانی که در 15 دقیقه اخیر آنلاین بوده اند وجود دارد."
	case "direct":
		text = "🔹پیام دایرکت چیست؟\n\nبا پیام دایرکت میتونی بصورت آنی به کاربر پیام متنی ارسال بکنی حتی اگه درحال چت کردن باشه !\n\nفقط کافیه وقتی پروفایل کاربر رو مشاهده میکنی روی گزینه 《📨 پیام دایرکت》 بزنی و متن پیامتو بفرستی.\n\n- درصورت ارسال پیام دایرکت 1 سکه ازت کم میشه\n- این پیام همون لحظه ارسال میشه و بعدا تو ربات آرشیو نمیشه.\n\n📢 توجه: متن پیام حداکثر میتونه 200 حرف باشه و اگه متنی که ارسال میکنی بیشتر از 200 حرف بود فقط 200 حرف اولش ارسال میشه.\n\n💥 قابلیت ویژه پیام دایرکت : درصورتی که کاربر دریافت کننده ، ربات را بلاک کرده باشد پیام دایرکت به محض آنبلاک شدن کاربر به او ارسال میگردد تا حتما پیام دایرکت را مشاهده کند."
	case "shortcuts":
		text = "🔹 چگونه با \"میان بر\" ها کار کنم؟\n\nمیانبر به شما امکان استفاده آسان و سریع از ربات رو میده !\n\nفقط کافیه وقتی توی ربات حرف 《 / 》 رو تایپ کنی تا لیست اصلی ترین میانبر ها رو ببینی.\n\nلیست میانبر های ربات 👇\n\n/start - ♻️ شروع از اول\n/sr - 🎲جستجوی شانسی\n/sg - 🙎‍♀️جستجوی دختر\n/sb - 🙎‍♂️جستجوی پسر\n/link - 💯ساخت لینک اختصاصی من\n/profile - 👤 پروفاـــــــیل من\n/credit -💰سکه های من\n/help - 🤔راهنما\n/id- مشاهده آیدی من"
	case "onw":
		text = "🔹 اطلاع رسانی آنلاین شدن مخاطب\n\nبا این قابلیت وقتی که کاربر مورد نظرت آنلاین شد ، بهت اطلاع رسانی میشه.\n\n- درصورت فعال کردن این قابلیت برای هر کاربر 1 💰سکه ازت کم میشه\n- این قابلیت یکبار فعال میشه و برای اطلاع رسانی دوباره باید یکبار دیگه فعالش کنی.\n- اگه بعد از 10 روز کاربر مورد نظرت آنلاین نشد این قابلیت غیر فعال میشه.\n\n🔴 برای فعال کردن این قابلیت گزینه «🔔 به محض آنلاین شدن  اطلاع بده » توی پروفایل کاربر مورد نظرت رو بزن .\n(در صورتی که این گزینه وجود نداره یا کاربر آنلاینه و یا این قابلیت رو قبلا براش فعال کردی)"
	case "chw":
		text = "🔹 اطلاع رسانی اتمام چت مخاطب\n\nبا این قابلیت وقتی که کاربر مورد نظرت چتش با مخاطبش تموم بشه ، بهت اطلاع رسانی میشه.\n\n- درصورت فعال کردن این قابلیت برای هر کاربر 1 💰سکه ازت کم میشه\n- این قابلیت یکبار فعال میشه و برای اطلاع رسانی دوباره باید یکبار دیگه فعالش کنی.\n- اگه بعد از 10 روز چت کاربر مورد نظرت تموم نشد این قابلیت غیر فعال میشه.\n\n🔴 برای فعال کردن این قابلیت گزینه «🔔 به محض اتمام چت اطلاع بده » توی پروفایل کاربر مورد نظرت رو بزن .\n(در صورتی که این گزینه وجود نداره یا کاربر درجال چت نیست و یا این قابلیت رو قبلا براش فعال کردی)"
	case "contacts":
		text = "🔹   لیست مخاطبین چیست ؟\n\n\nبا قابلیت لیست مخاطبین می تونی مخاطب هاتو تو ربات داشته باشی و گمشون نکنی !\n\n- برای دیدن لیست مخاطبین خودت کافیه 《👤 پروفایل》 رو از منوی ربات انتخاب کنی و «🙎‍♂️🙎‍♀️ لیست مخاطبین» رو لمس کنی.\nو یا « /contacts » رو از منوی میانبر ها انتخاب کنی.\n\nبرای اضافه کردن کاربر به لیست مخاطبین گزینه «➕افزودن به مخاطبین » رو تو پروفایلش بزن."
	case "search":
		text = "🔹  چگونه بصورت پیشرفته بین کاربران جستجو کنم ؟\n\nکافیه گزینه «🔍 جستجوی کاربران 🔎» رو تو منوی ربات لمس کنی و یکی از این گزینه ها رو انتخاب کنی 👇\n\n🎌 هم استانی ها\n - لیست کاربرانی که تو استان شما هستند.\n\n 👥 هم سن ها\n - لیست کاربرانی که در رده سنی نزدیک شما هستند.\n\n🚶‍♂️ بدون چت ها 🚶‍♀️\n - لیست کاربرانی آنلاینی که در حال چت نیستند.\n\n🙋‍♂️ کاربران جدید 🙋‍♀️\n - لیست کاربراننی که تازه عضو ربات شده اند.\n\n 🔍 جستجوی پیشرفته 🔎\n - جستجوی کاربران بصورت پیشرفته با فیلتر جنسیت ، رده سنی ، استان ها و افراد نزدیک ، تاریخ آخرین فعالیت در ربات و با قابلیت مرتب سازی بر اساس نزدیک بودن فاصله ، تاریخ آخرین فعالیت در ربات ، سن و نمایش کاربران بدون چ آنلاین !\n\n<code>استفاده از این امکانات بصورت کاملا رایگان و بدون نیاز به سکه است.</code>"
	case "deleteMessage":
		text = "🔹  آموزش حذف پیام در چت\n\nهر پیامی که تو چت فرستادی و میخوای حذفش کنی کافیه ریپلایش کنی و کلمه «del» یا «حذف» رو تایپ کنی تا از چت مخاطبت حذف بشه."
	default:
		return false, nil
	}
	text = helpAnchorPattern.ReplaceAllString(text, "")
	// All help topics are now sent as a single message (text or image with caption)
	if imageName != "" {
		return true, s.sendHelpImage(ctx, c, imageName, text, markup)
	}
	params := map[string]any{
		"chat_id":             c.UserID,
		"text":                text,
		"reply_to_message_id": c.MessageID,
		"parse_mode":          "HTML",
	}
	if markup != "" {
		params["reply_markup"] = markup
	}
	_, err := s.send(ctx, "sendMessage", params)
	return true, err
}

var helpAnchorPattern = regexp.MustCompile(`<a href='[^']*'>[^<]*</a>`)

func (s *Service) invite(ctx context.Context, c *UpdateContext) error {
	inviteLink := "https://t.me/" + s.cfg.BotUsername + "?start=r_" + c.User.UniqID
	resp, _ := s.send(ctx, "sendPhoto", map[string]any{
		"chat_id": c.UserID,
		"photo":   s.asset("invite.jpg"),
		"caption": "《" + s.cfg.BotName + " 🤖》 هستم،بامن میتونی\n\n" +
			"📡 افراد #نزدیک ، #هم‌سنی ، #هم‌استانی خودتو پیداکنی و باهاش #ناشناس چت کنی و آشنا شی😍\n\n" +
			"پس منتظر چی هستی؟🤔 بدووو بیا که منتظرتم!🏃‍♂️\n\n" +
			"همین الان روی لینک بزن  👇\n" +
			inviteLink + "\n\n" +
			"✅ #رایگان و #واقعی 😎",
		"parse_mode": "HTML",
	})
	replyTo := 0
	if msg, ok := s.tg.SentMessage(resp); ok {
		replyTo = msg.MessageID
	}
	var count int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM users WHERE referral=$1`, c.UserID).Scan(&count)
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": fmt.Sprintf("لینک⚡️ دعوت شما با موفقیت ساخته شد 👆\n\n%s\n\n<code>شما می‌توانید لینک را برای گروه‌ها و دوستان خود ارسال کنید.</code>\n\nبا معرفی هر نفر %d سکه بگیرید! برای اطلاعات بیشتر راهنمای سکه را بخوانید (/help_credit)\n\n👈 شما تاکنون %d نفر را دعوت کرده‌اید.",
			inviteLink, c.Admin.CoinPerInvite+c.Admin.CoinPerInviteProfile+c.Admin.CoinPerInviteInvite, count),
		"reply_to_message_id": replyTo,
		"parse_mode":          "HTML",
	})
	return err
}
