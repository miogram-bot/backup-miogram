package telegram

import (
	"encoding/json"
	"strings"
)

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
	InlineQuery   *InlineQuery   `json:"inline_query,omitempty"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Message struct {
	MessageID       int             `json:"message_id"`
	From            *User           `json:"from,omitempty"`
	Chat            Chat            `json:"chat"`
	Text            string          `json:"text,omitempty"`
	Caption         string          `json:"caption,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	CaptionEntities []MessageEntity `json:"caption_entities,omitempty"`
	Photo           []PhotoSize     `json:"photo,omitempty"`
	Audio           *File           `json:"audio,omitempty"`
	Video           *File           `json:"video,omitempty"`
	Voice           *File           `json:"voice,omitempty"`
	Document        *File           `json:"document,omitempty"`
	Animation       *File           `json:"animation,omitempty"`
	Sticker         *File           `json:"sticker,omitempty"`
	Contact         *Contact        `json:"contact,omitempty"`
	Location        *Location       `json:"location,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
}

type MessageEntity struct {
	Type string `json:"type"`
}

type PhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type File struct {
	FileID string `json:"file_id"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name,omitempty"`
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type InlineQuery struct {
	ID       string `json:"id"`
	From     User   `json:"from"`
	Query    string `json:"query"`
	Offset   string `json:"offset"`
	ChatType string `json:"chat_type,omitempty"`
}

type APIResponse struct {
	Ok          bool               `json:"ok"`
	ErrorCode   int                `json:"error_code,omitempty"`
	Description string             `json:"description,omitempty"`
	Result      json.RawMessage    `json:"result,omitempty"`
	Parameters  ResponseParameters `json:"parameters,omitempty"`
}

// NeedsStart reports failures where the destination user has never opened the
// target bot (/start missing). These messages go to the pending delivery
// queue and flush once the user starts that bot.
func (r APIResponse) NeedsStart() bool {
	if r.Ok {
		return false
	}
	switch r.ErrorCode {
	case 400:
		return strings.Contains(r.Description, "chat not found")
	case 403:
		d := strings.ToLower(r.Description)
		if strings.Contains(d, "bot was blocked by the user") {
			return false
		}
		return strings.Contains(d, "can't initiate conversation with a user") ||
			strings.Contains(d, "upgraded to a business account")
	}
	return false
}

// PermanentlyUndeliverable reports failures where queueing is pointless
// because the user blocked the bot or deleted their account.
func (r APIResponse) PermanentlyUndeliverable() bool {
	if r.Ok {
		return false
	}
	d := strings.ToLower(r.Description)
	return strings.Contains(d, "bot was blocked by the user") ||
		strings.Contains(d, "user is deactivated") ||
		strings.Contains(d, "user was deactivated")
}

type ResponseParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

type SentMessage struct {
	MessageID int         `json:"message_id"`
	Photo     []PhotoSize `json:"photo,omitempty"`
}

type InputMessageContent struct {
	MessageText           string `json:"message_text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview,omitempty"`
}

type InlineQueryResultArticle struct {
	Type                string              `json:"type"`
	ID                  string              `json:"id"`
	Title               string              `json:"title"`
	ThumbURL            string              `json:"thumb_url,omitempty"`
	Description         string              `json:"description,omitempty"`
	ParseMode           string              `json:"parse_mode,omitempty"`
	InputMessageContent InputMessageContent `json:"input_message_content"`
}

type LocalFile struct {
	Path string
}
