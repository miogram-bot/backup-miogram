package bot

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"

	"miogram/internal/storage"
	"miogram/internal/telegram"
)

func (s *Service) handleChat(ctx context.Context, c *UpdateContext) (bool, error) {
	chat, err := s.store.ActiveChat(ctx, c.UserID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		log.Printf("[handleChat] Error getting active chat for user %s: %v", c.UserID, err)
		return false, err
	}

	otherID := chat.UserID1
	if otherID == c.UserID {
		otherID = chat.UserID2
	}
	other, err := s.store.UserByID(ctx, otherID)
	if err != nil {
		log.Printf("[handleChat] Error getting other user %s: %v", otherID, err)
		return true, err
	}

	if c.Text == PrivateChatButton {
		return true, s.toggleChatPrivacy(ctx, c, chat)
	}

	if c.Text == TicTacToeButton {
		return true, s.startTicTacToe(ctx, c, chat, other)
	}

	if part(c.ExData, 0) == "ttt" {
		return true, s.playTicTacToe(ctx, c, chat)
	}

	if c.Text == EndChatButton {
		if remaining := 10 - (c.Now - chat.StartedAt); remaining > 0 {
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text":    fmt.Sprintf("⚠️ حداقل ۱۰ ثانیه پس از اتصال امکان پایان گفتگو وجود دارد.\n\n⏳ %d ثانیه دیگر صبر کنید.", remaining),
			})
			return true, err
		}
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "🤖 پیام سیستم 👇\n\n" +
				"مطمئنی میخوای چت رو قطع کنی؟",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{{
				callbackButton("❌ اتمام چت", "endchat;"+other.UniqID+";end"),
				callbackButton("🗣 ادامه چت", "endchat;"+other.UniqID+";continue"),
			}})),
		})
		return true, err
	}

	if part(c.ExData, 0) == "endchat" {
		_, err := s.handleEndChat(ctx, c, chat, other)
		return true, err
	}

	if c.Text == "/start" {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "🤖 پیام سیستم 👇\n\n" +
				"<code>    - ❗️اول باید مکالمه جاری رو قطع کنی بعدا 《استارت》 بزنی 👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup":        telegram.JSON(replyMarkupKeyboard(chatKeyboard())),
		})
		return true, err
	}

	if BotButtons[c.Text] || c.Callback != nil {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "⚠️ خطا: هم اکنون شما در حال چت با /user_" + other.UniqID + " هستید !\n\n" +
				"<code>برای استفاده از ربات ابتدا باید مکالمه رو قطع کنی 👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup":        telegram.JSON(replyMarkupKeyboard(chatKeyboard())),
		})
		return true, err
	}

	if hasForbiddenEntity(c.Entities) {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "🤖 پیام سیستم 👇\n\n" +
				"⚠️ خطا: امکان ارسال لینک وجود ندارد.\n\n" +
				"<code>- برای ارسال لینک یا آیدی تلگرام از پیام دایرکت استفاده کنید.</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
		})
		return true, err
	}

	replyTo := 0
	targetID := other.UserID
	if c.Message != nil && c.Message.ReplyToMessage != nil {
		msgMap, found, err := s.findReplyMessage(ctx, c.UserID, c.Message.ReplyToMessage.MessageID)
		if err != nil {
			return true, err
		}
		if found {
			if c.Text == "حذف" {
				s.deleteMessage(ctx, c.UserID, c.Message.ReplyToMessage.MessageID)
				s.deleteMessage(ctx, msgMap.UserID2, msgMap.MessageID2)
				_, err := s.send(ctx, "sendMessage", map[string]any{
					"chat_id":             c.UserID,
					"text":                "✔️ با موفقیت حذف شد.",
					"reply_to_message_id": c.MessageID,
				})
				return true, err
			}
			targetID = msgMap.UserID2
			replyTo = msgMap.MessageID2
		}
	}

	if c.EditedMessage != nil {
		return true, s.editChatMessage(ctx, c, other)
	}

	return true, s.relayChatMessage(ctx, c, targetID, replyTo, other)
}

// handleEndChat handles the end chat callback with proper error handling and logging
func (s *Service) handleEndChat(ctx context.Context, c *UpdateContext, chat storage.Chat, other storage.User) (bool, error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[handleEndChat] PANIC: %v, user: %s, chat: %d", r, c.UserID, chat.ID)
		}
	}()

	action := part(c.ExData, 2)

	if action == "continue" {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text":    "✅ ادامه چت",
		})
		return true, err
	}

	if action == "end" {
		// Check minimum chat duration
		if remaining := 10 - (c.Now - chat.StartedAt); remaining > 0 {
			log.Printf("[handleEndChat] User %s tried to end chat too early, %d seconds remaining", c.UserID, remaining)
			return true, s.answer(ctx, c, fmt.Sprintf("هنوز %d ثانیه تا امکان پایان گفتگو باقی مانده است.", remaining))
		}

		// Delete the confirmation message
		s.deleteMessage(ctx, c.UserID, c.MessageID)

		// Log the end chat attempt
		log.Printf("[handleEndChat] Ending chat %d between %s and %s, initiated by %s",
			chat.ID, chat.UserID1, chat.UserID2, c.UserID)

		// End the chat with proper error handling
		ended, endErr := s.store.EndChat(ctx, chat.ID, c.Now)
		if endErr != nil {
			log.Printf("[handleEndChat] ERROR ending chat %d for user %s: %v", chat.ID, c.UserID, endErr)

			// Try to recover by updating user status directly
			if _, err := s.store.DB().Exec(ctx, `UPDATE users SET step='start' WHERE user_id=$1 AND step LIKE 'chatting;%'`, c.UserID); err != nil {
				log.Printf("[handleEndChat] Failed to update user status after error: %v", err)
			}

			_ = s.answer(ctx, c, "❌ خطا در پایان چت، لطفاً دوباره تلاش کنید.")
			return true, nil
		}

		// Log successful end chat
		log.Printf("[handleEndChat] Successfully ended chat %d, messages: %d, refund1: %d, refund2: %d",
			chat.ID, ended.Messages, ended.Refund1, ended.Refund2)

		// Calculate refunds
		currentRefund, otherRefund := ended.Refund1, ended.Refund2
		if ended.UserID2 == c.UserID {
			currentRefund, otherRefund = ended.Refund2, ended.Refund1
		}

		// Send end chat message to current user
		text := endChatText(other.UniqID, false, currentRefund)
		if c.User.Balance > 0 {
			text += fmt.Sprintf("\n💰 موجودی فعلی شما: %d سکه", c.User.Balance)
		}

		_, sendErr := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":      c.UserID,
			"text":         text,
			"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
		})
		if sendErr != nil {
			log.Printf("[handleEndChat] Error sending end message to %s: %v", c.UserID, sendErr)
		}

		// Send end chat message to other user if not fake
		if !other.IsFake {
			// Reload other user to get updated balance
			otherUser, err := s.store.UserByID(ctx, other.UserID)
			if err != nil {
				log.Printf("[handleEndChat] Error reloading other user %s: %v", other.UserID, err)
			}

			otherText := endChatText(c.User.UniqID, true, otherRefund)
			if otherUser.Balance > 0 {
				otherText += fmt.Sprintf("\n💰 موجودی فعلی شما: %d سکه", otherUser.Balance)
			}

			_, sendErr = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":      other.UserID,
				"text":         otherText,
				"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(other.UserID == s.cfg.AdminID || other.Status == "admin"))),
			})
			if sendErr != nil {
				log.Printf("[handleEndChat] Error sending end message to other user %s: %v", other.UserID, sendErr)
			}
		}

		// Flush end chat notifications
		if err := s.flushEndChatNotifications(ctx, other.UserID, other.UniqID); err != nil {
			log.Printf("[handleEndChat] Error flushing notifications for %s: %v", other.UserID, err)
		}
		if err := s.flushEndChatNotifications(ctx, c.UserID, c.User.UniqID); err != nil {
			log.Printf("[handleEndChat] Error flushing notifications for %s: %v", c.UserID, err)
		}

		// Update user status
		if err := s.reloadUser(ctx, c); err != nil {
			log.Printf("[handleEndChat] Error reloading user %s: %v", c.UserID, err)
		}

		return true, nil
	}

	log.Printf("[handleEndChat] Unknown action: %s", action)
	return true, nil
}

func endChatText(otherUniq string, endedByOther bool, refund int) string {
	who := "توسط شما قطع شد"
	if endedByOther {
		who = "توسط کاربر مقابل قطع شد"
	}
	text := "چت شما با /user_" + otherUniq + " " + who + "\n\n" +
		"برای گزارش عدم رعایت قوانین (/help_terms) می‌توانید با لمس 《 🚫 گزارش کاربر 》 در پروفایل، کاربر را گزارش کنید.\n" +
		"🗑 تا ۳۰ دقیقه بعد از اتمام چت می‌توانید با دستور زیر پیام‌های ارسال‌شده را برای هر دو طرف پاک کنید:\n" +
		"/delete_messages_" + otherUniq
	if refund > 0 {
		text += fmt.Sprintf("\n💰 تعداد %d سکه به دلیل ناموفق بودن چت به حساب شما بازگشت.", refund)
	}
	return text
}

func (s *Service) flushEndChatNotifications(ctx context.Context, targetID, targetUniq string) error {
	rows, err := s.store.DB().Query(ctx, `SELECT id,user_id FROM notif WHERE user_id_2=$1 AND reason='endchatnotif'`, targetID)
	if err != nil {
		log.Printf("[flushEndChatNotifications] Error querying notifications for %s: %v", targetID, err)
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
			log.Printf("[flushEndChatNotifications] Error scanning notification row: %v", err)
			return err
		}
		items = append(items, it)
	}

	for _, it := range items {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": it.UserID,
			"text":    "🔔 هم اکنون چت کاربر /user_" + targetUniq + " به پایان رسید.",
		})
		if err != nil {
			log.Printf("[flushEndChatNotifications] Error sending notification to %s: %v", it.UserID, err)
		}
		_, err = s.store.DB().Exec(ctx, `DELETE FROM notif WHERE id=$1`, it.ID)
		if err != nil {
			log.Printf("[flushEndChatNotifications] Error deleting notification %d: %v", it.ID, err)
		}
	}
	return rows.Err()
}

func hasForbiddenEntity(entities []telegram.MessageEntity) bool {
	for _, entity := range entities {
		switch entity.Type {
		case "url", "text_link", "email", "mention", "text_mention":
			return true
		}
	}
	return false
}

func (s *Service) findReplyMessage(ctx context.Context, userID string, messageID int) (storage.ChatMessage, bool, error) {
	var row storage.ChatMessage
	err := s.store.DB().QueryRow(ctx, `SELECT id,user_id_1,message_id_1,user_id_2,message_id_2,created_at FROM chatmsgs WHERE user_id_1=$1 AND message_id_1=$2 LIMIT 1`, userID, messageID).
		Scan(&row.ID, &row.UserID1, &row.MessageID1, &row.UserID2, &row.MessageID2, &row.CreatedAt)
	if err == nil {
		return row, true, nil
	}
	if err != pgx.ErrNoRows {
		log.Printf("[findReplyMessage] Error querying message for user %s: %v", userID, err)
		return row, false, err
	}
	err = s.store.DB().QueryRow(ctx, `SELECT id,user_id_2,message_id_2,user_id_1,message_id_1,created_at FROM chatmsgs WHERE user_id_2=$1 AND message_id_2=$2 LIMIT 1`, userID, messageID).
		Scan(&row.ID, &row.UserID1, &row.MessageID1, &row.UserID2, &row.MessageID2, &row.CreatedAt)
	if err == pgx.ErrNoRows {
		return row, false, nil
	}
	if err != nil {
		log.Printf("[findReplyMessage] Error querying message for user %s: %v", userID, err)
	}
	return row, err == nil, err
}

func (s *Service) editChatMessage(ctx context.Context, c *UpdateContext, other storage.User) error {
	var row storage.ChatMessage
	err := s.store.DB().QueryRow(ctx, `SELECT id,user_id_1,message_id_1,user_id_2,message_id_2,created_at FROM chatmsgs WHERE user_id_1=$1 AND message_id_1=$2 LIMIT 1`, c.UserID, c.MessageID).
		Scan(&row.ID, &row.UserID1, &row.MessageID1, &row.UserID2, &row.MessageID2, &row.CreatedAt)
	if err != nil {
		log.Printf("[editChatMessage] Error finding message for user %s: %v", c.UserID, err)
		return nil
	}

	if c.EditedMessage.Text != "" {
		_, err := s.send(ctx, "editMessageText", map[string]any{
			"chat_id":    row.UserID2,
			"text":       c.Text + "\n\n📝 ویرایش شده در (" + toEnglish(jdate(s.loc, "Y-m-d H:i", c.Now)) + ")",
			"message_id": row.MessageID2,
		})
		if err != nil {
			log.Printf("[editChatMessage] Error editing text message: %v", err)
		}
		return err
	}

	mediaType, fileID := editedMedia(c.EditedMessage)
	if mediaType == "" {
		return nil
	}
	_, err = s.send(ctx, "editMessageMedia", map[string]any{
		"chat_id": row.UserID2,
		"media": telegram.JSON(map[string]any{
			"type":    mediaType,
			"media":   fileID,
			"caption": c.EditedMessage.Caption + "\n\n📝 ویرایش شده در (" + toEnglish(jdate(s.loc, "Y-m-d H:i", c.Now)) + ")",
		}),
		"message_id":   row.MessageID2,
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("⚠️ گزارش", "gib;report;"+other.UniqID+";repchat")}})),
	})
	if err != nil {
		log.Printf("[editChatMessage] Error editing media message: %v", err)
	}
	return err
}

func editedMedia(m *telegram.Message) (string, string) {
	if m == nil {
		return "", ""
	}
	for _, typ := range []string{"photo", "audio", "video", "animation", "document"} {
		if id := fileIDByType(m, typ); id != "" {
			return typ, id
		}
	}
	return "", ""
}

func (s *Service) relayChatMessage(ctx context.Context, c *UpdateContext, targetID string, replyTo int, other storage.User) error {
	method := ""
	params := map[string]any{"chat_id": targetID}
	chat, err := s.store.ActiveChat(ctx, c.UserID)
	if err == nil && s.chatPrivacyEnabled(ctx, chat.ID, c.UserID) {
		params["protect_content"] = true
	}
	if replyTo != 0 {
		params["reply_to_message_id"] = replyTo
	}
	if c.Message == nil {
		return nil
	}

	isMedia := false
	var fileID string
	var mediaType string

	switch {
	case c.Message.Text != "":
		method = "sendMessage"
		params["text"] = c.Text
	case len(c.Message.Photo) > 0:
		method = "sendPhoto"
		params["photo"] = photoID(c.Message)
		params["caption"] = c.Message.Caption
		isMedia = true
		fileID = photoID(c.Message)
		mediaType = "photo"
	case c.Message.Audio != nil:
		method = "sendAudio"
		params["audio"] = c.Message.Audio.FileID
		params["caption"] = c.Message.Caption
		isMedia = true
		fileID = c.Message.Audio.FileID
		mediaType = "audio"
	case c.Message.Video != nil:
		method = "sendVideo"
		params["video"] = c.Message.Video.FileID
		params["caption"] = c.Message.Caption
		isMedia = true
		fileID = c.Message.Video.FileID
		mediaType = "video"
	case c.Message.Animation != nil:
		method = "sendAnimation"
		params["animation"] = c.Message.Animation.FileID
		params["caption"] = c.Message.Caption
		isMedia = true
		fileID = c.Message.Animation.FileID
		mediaType = "animation"
	case c.Message.Document != nil:
		method = "sendDocument"
		params["document"] = c.Message.Document.FileID
		params["caption"] = c.Message.Caption
		isMedia = true
		fileID = c.Message.Document.FileID
		mediaType = "document"
	case c.Message.Voice != nil:
		method = "sendVoice"
		params["voice"] = c.Message.Voice.FileID
		params["caption"] = c.Message.Caption
		isMedia = true
		fileID = c.Message.Voice.FileID
		mediaType = "voice"
	case c.Message.Contact != nil:
		method = "sendContact"
		params["phone_number"] = c.Message.Contact.PhoneNumber
	case c.Message.Location != nil:
		method = "sendLocation"
		params["latitude"] = c.Message.Location.Latitude
		params["longitude"] = c.Message.Location.Longitude
	case c.Message.Sticker != nil:
		method = "sendSticker"
		params["sticker"] = c.Message.Sticker.FileID
		isMedia = true
		fileID = c.Message.Sticker.FileID
		mediaType = "sticker"
	default:
		return nil
	}

	if method != "sendMessage" {
		params["reply_markup"] = telegram.JSON(replyMarkupInline([][]button{{callbackButton("⚠️ گزارش", "gib;report;"+other.UniqID+";repchat")}}))
	}

	// Media is blocked before delivery for the first three minutes.
	if isMedia && chat.StartedAt > 0 {
		elapsed := c.Now - chat.StartedAt
		if elapsed < 180 {
			remaining := 180 - elapsed
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                fmt.Sprintf("⚠️ محدودیت ارسال فایل\n\nارسال تصویر، ویدیو، گیف، استیکر و فایل صوتی در ۳ دقیقه اول گفتگو امکان‌پذیر نیست.\n\n⏳ زمان باقی‌مانده: %d ثانیه", remaining),
				"reply_to_message_id": c.MessageID,
			})
			if err != nil {
				log.Printf("[relayChatMessage] Error sending media restriction message: %v", err)
			}
			return nil
		}
	}

	if other.IsFake {
		_, err = s.store.DB().Exec(ctx, `INSERT INTO chatmsgs (chat_id,user_id_1,message_id_1,user_id_2,message_id_2,created_at) VALUES ($1,$2,$3,$4,0,$5)`, chat.ID, c.UserID, c.MessageID, targetID, c.Now)
		if err != nil {
			log.Printf("[relayChatMessage] Error inserting fake chat message: %v", err)
			return err
		}
		return nil
	}

	// Thread the original sender so Case C timeout fallbacks can notify them
	// if this message can never be delivered (send() strips the marker).
	params[fromUserParam] = c.UserID
	resp, err := s.send(ctx, method, params)
	if err != nil {
		log.Printf("[relayChatMessage] Error sending message to %s: %v", targetID, err)
		return err
	}

	if msg, ok := s.tg.SentMessage(resp); ok {
		_, err = s.store.DB().Exec(ctx, `INSERT INTO chatmsgs (chat_id,user_id_1,message_id_1,user_id_2,message_id_2,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, chat.ID, c.UserID, c.MessageID, targetID, msg.MessageID, c.Now)
		if err != nil {
			log.Printf("[relayChatMessage] Error inserting chat message: %v", err)
		}
	}

	// Forward media files to admin group topic 49 for monitoring
	if isMedia && fileID != "" {
		senderUsername := getUserUsername(ctx, s, c.UserID)
		receiverUsername := getUserUsername(ctx, s, other.UserID)

		mediaCaption := fmt.Sprintf("📤 ارسال کننده:\n/user_%s\n/user_%s\n%s\n\n📥 دریافت کننده:\n/user_%s\n/user_%s\n%s",
			c.UserID, c.User.UniqID, senderUsername,
			other.UserID, other.UniqID, receiverUsername)

		adminGroupID := s.adminGroupID(ctx)
		if adminGroupID != "" {
			switch mediaType {
			case "photo":
				_, _ = s.send(ctx, "sendPhoto", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"photo":             fileID,
					"caption":           mediaCaption,
					"parse_mode":        "HTML",
				})
			case "audio":
				_, _ = s.send(ctx, "sendAudio", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"audio":             fileID,
					"caption":           mediaCaption,
					"parse_mode":        "HTML",
				})
			case "video":
				_, _ = s.send(ctx, "sendVideo", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"video":             fileID,
					"caption":           mediaCaption,
					"parse_mode":        "HTML",
				})
			case "animation":
				_, _ = s.send(ctx, "sendAnimation", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"animation":         fileID,
					"caption":           mediaCaption,
					"parse_mode":        "HTML",
				})
			case "document":
				_, _ = s.send(ctx, "sendDocument", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"document":          fileID,
					"caption":           mediaCaption,
					"parse_mode":        "HTML",
				})
			case "voice":
				_, _ = s.send(ctx, "sendVoice", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"voice":             fileID,
					"caption":           mediaCaption,
					"parse_mode":        "HTML",
				})
			case "sticker":
				_, _ = s.send(ctx, "sendSticker", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"sticker":           fileID,
				})
				_, _ = s.send(ctx, "sendMessage", map[string]any{
					"chat_id":           adminGroupID,
					"message_thread_id": adminTopicUserFiles,
					"text":              mediaCaption,
				})
			}
		}
	}

	return err
}

func (c *UpdateContext) String() string {
	return fmt.Sprintf("user=%s msg=%d data=%q text=%q", c.UserID, c.MessageID, c.Data, c.Text)
}
