package bot

import (
	"strconv"
	"strings"

	"miogram/internal/storage"
	"miogram/internal/telegram"
)

type UpdateContext struct {
	Update        telegram.Update
	Message       *telegram.Message
	EditedMessage *telegram.Message
	Callback      *telegram.CallbackQuery
	Inline        *telegram.InlineQuery

	UserID    string
	ChatID    string
	FirstName string
	Username  string
	Text      string
	Caption   string
	TextCap   string
	Data      string
	CQID      string
	MessageID int
	Entities  []telegram.MessageEntity
	ExText    []string
	ExData    []string
	ExStep    []string
	Now       int64
	BotID     string // اضافه شد

	User  storage.User
	Admin storage.Admin
}

func newUpdateContext(up telegram.Update, now int64) (UpdateContext, bool) {
	c := UpdateContext{Update: up, Now: now}
	if up.Message != nil {
		m := up.Message
		c.Message = m
		if m.From == nil {
			return c, false
		}
		c.UserID = strconv.FormatInt(m.From.ID, 10)
		c.ChatID = strconv.FormatInt(m.Chat.ID, 10)
		c.FirstName = m.From.FirstName
		if m.From.Username != "" {
			c.Username = "@" + m.From.Username
		}
		c.Text = m.Text
		c.Caption = m.Caption
		c.TextCap = m.Text
		c.Entities = m.Entities
		if c.Text == "" && c.Caption != "" {
			c.TextCap = c.Caption
			c.Entities = m.CaptionEntities
		}
		c.MessageID = m.MessageID
		c.ExText = strings.Split(c.Text, "_")
		return c, true
	}
	if up.EditedMessage != nil {
		m := up.EditedMessage
		c.EditedMessage = m
		if m.From == nil {
			return c, false
		}
		c.UserID = strconv.FormatInt(m.From.ID, 10)
		c.ChatID = strconv.FormatInt(m.Chat.ID, 10)
		if m.From.Username != "" {
			c.Username = "@" + m.From.Username
		}
		c.Text = m.Text
		c.Caption = m.Caption
		c.TextCap = m.Text
		c.Entities = m.Entities
		if c.Text == "" && c.Caption != "" {
			c.TextCap = c.Caption
			c.Entities = m.CaptionEntities
		}
		c.MessageID = m.MessageID
		c.ExText = strings.Split(c.Text, "_")
		return c, true
	}
	if up.CallbackQuery != nil {
		cb := up.CallbackQuery
		c.Callback = cb
		c.UserID = strconv.FormatInt(cb.From.ID, 10)
		c.FirstName = cb.From.FirstName
		if cb.From.Username != "" {
			c.Username = "@" + cb.From.Username
		}
		c.Data = cb.Data
		c.CQID = cb.ID
		c.ExData = strings.Split(cb.Data, ";")
		if cb.Message != nil {
			c.MessageID = cb.Message.MessageID
			c.ChatID = strconv.FormatInt(cb.Message.Chat.ID, 10)
		} else {
			c.ChatID = c.UserID
		}
		return c, true
	}
	if up.InlineQuery != nil {
		iq := up.InlineQuery
		c.Inline = iq
		c.UserID = strconv.FormatInt(iq.From.ID, 10)
		c.FirstName = iq.From.FirstName
		if iq.From.Username != "" {
			c.Username = "@" + iq.From.Username
		}
		c.ChatID = c.UserID
		return c, true
	}
	return c, false
}

func (c *UpdateContext) refreshStep() {
	c.ExStep = strings.Split(c.User.Step, ";")
}
