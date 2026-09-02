package bot

import (
	"context"

	"miogram/internal/telegram"
)

func (s *Service) handleStart(ctx context.Context, c *UpdateContext) (bool, error) {
	backToStart := c.Text == BackButton && (c.User.PrevStep == "start" || c.User.PrevStep == "complete_profile" || part(c.ExStep, 0) == "connect" || (part(c.ExStep, 0) == "profile" && part(c.ExStep, 1) == "none") || part(c.ExStep, 1) == "selectGender")
	if c.Text == "/start" || c.Text == MainButton || c.Data == "start" || backToStart {
		if c.Data == "start" {
			s.deleteMessage(ctx, c.UserID, c.MessageID)
			text := mainMenuText()
			if c.User.Step == "start" {
				text = "متوجه نشدم :/\n\n<code>چه کاری برات انجام بدم؟ از منوی پایین انتخاب کن 👇</code>"
			}
			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":      c.UserID,
				"text":         text,
				"parse_mode":   "HTML",
				"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})
			_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
			return true, err
		}
		return true, s.mainMenu(ctx, c, true)
	}
	return false, nil
}
