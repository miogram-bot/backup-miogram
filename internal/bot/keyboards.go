package bot

import (
	"fmt"

	"miogram/internal/storage"
)

type button map[string]any

// Button is the exported alias of button so other packages can build keyboards
// to pass into Service.SendMessageWithRoutingAndKeyboard.
type Button = button

func textButton(text string) button {
	return button{"text": text}
}

func callbackButton(text, data string) button {
	return button{"text": text, "callback_data": data}
}

func urlButton(text, url string) button {
	return button{"text": text, "url": url}
}

func copyButton(text, value string) button {
	return button{"text": text, "copy_text": map[string]string{"text": value}}
}

func switchButton(text, query string) button {
	return button{"text": text, "switch_inline_query_current_chat": query}
}

func mainMenuKeyboard(isAdmin bool) [][]button {
	rows := [][]button{
		{textButton("🔗 به یه ناشناس وصلم کن!️")},
		{textButton("🔍 جستجوی کاربران 🔎"), textButton("📍افراد نزدیک")},
		{textButton("💰سکه"), textButton("👤پروفایل"), textButton("👨‍💻پشتیبانی")},
		{textButton("🚸 معرفی به دوستان (سکه رایگان)")},
	}
	if isAdmin {
		rows = append(rows, []button{textButton("👤 پنل ادمین 👤")})
	}
	return rows
}

func chatKeyboard() [][]button {
	return [][]button{
		{textButton(ShowProfileButton)},
		{textButton(PrivateChatButton), textButton(TicTacToeButton)},
		{textButton(EndChatButton)},
	}
}

func ageKeyboard() [][]button {
	rows := [][]int{
		{9, 10, 11, 12, 13, 14, 15},
		{16, 17, 18, 19, 20, 21, 22},
		{23, 24, 25, 26, 27, 28, 29},
		{30, 31, 32, 33, 34, 35, 36},
		{37, 38, 39, 40, 41, 42, 43},
		{44, 45, 46, 47, 48, 49, 50},
		{51, 52, 53, 54, 55, 56, 57},
		{58, 59, 60, 61, 62, 63, 64},
		{65, 66, 67, 68, 69, 70, 71},
		{72, 73, 74, 75, 76, 77, 78},
		{79, 80, 81, 82, 83, 84, 85},
		{86, 87, 88, 89, 90, 91, 92},
		{93, 94, 95, 96, 97, 98, 99},
	}
	out := make([][]button, 0, len(rows))
	for _, row := range rows {
		btns := make([]button, 0, len(row))
		for _, n := range row {
			btns = append(btns, textButton(fmt.Sprint(n)))
		}
		out = append(out, btns)
	}
	return out
}

func ageInlineKeyboard(prefix string) [][]button {
	out := [][]button{}
	row := []button{}
	for n := 9; n <= 99; n++ {
		row = append(row, callbackButton(fmt.Sprint(n), fmt.Sprintf(prefix, n)))
		if len(row) == 7 {
			out = append(out, row)
			row = []button{}
		}
	}
	if len(row) > 0 {
		out = append(out, row)
	}
	return out
}

func statesReplyKeyboard(states []storage.State, includeBack bool) [][]button {
	rows := [][]button{}
	if includeBack {
		rows = append(rows, []button{textButton(BackButton)})
	}
	row := []button{}
	for _, st := range states {
		row = append(row, textButton(st.Name))
		if len(row) == 2 {
			rows = append(rows, row)
			row = []button{}
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

func provinceInlineKeyboard(states []storage.State) [][]button {
	rows := [][]button{}
	row := []button{}
	for _, st := range states {
		row = append(row, callbackButton(st.Name, fmt.Sprintf("province;state;%d", st.ID)))
		if len(row) == 2 {
			rows = append(rows, row)
			row = []button{}
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []button{callbackButton("🔙 بازگشت", "anon")})
	return rows
}

// anonymousSearchKeyboard - منوی جستجوی ناشناس (🔗 به یه ناشناس وصلم کن!️)
// جستجوی استان به این منو اضافه شده است
func anonymousSearchKeyboard() [][]button {
	return [][]button{
		{callbackButton("🎲 جستجوی شانسی 🎲", "anon;all;none")},
		{callbackButton("جستجوی پسر 🙋‍♂️", "anon;boy;none"), callbackButton("جستجوی دختر 🙋‍♀️", "anon;girl;none")},
		{callbackButton("🛰 جستجوی اطراف", "anon;gps;none")},
		{callbackButton("🌐جستجو بر پایه استان🎯", "province;select")},
	}
}

// registrationSearchKeyboard - منوی جستجوی بعد از ثبت‌نام
// با سه دکمه شیشه‌ای برای جستجوی شانسی، دختر، پسر
func registrationSearchKeyboard() [][]button {
	return [][]button{
		{callbackButton("🎲 جستجوی شانسی 🎲", "anon;all;none")},
		{callbackButton("جستجوی دختر 🙋‍♀️", "anon;girl;none"), callbackButton("جستجوی پسر 🙋‍♂️", "anon;boy;none")},
	}
}

// searchKeyboard - منوی جستجوی کاربران (🔍 جستجوی کاربران 🔎)
// دکمه جستجوی استان از این منو حذف شده است
func searchKeyboard() [][]button {
	return [][]button{
		{callbackButton("👥 هم سنی ها", "search;sage;none"), callbackButton("🎌 هم استانی ها", "search;sstate;none")},
		{callbackButton("🔍 جستجوی پیشرفته 🔎", "searchadv;none")},
		{callbackButton("🚶‍♂️بدون چت ها🚶‍♀️", "search;wchat;none"), callbackButton("🙋‍♂️ کاربران جدید 🙋‍♀️", "search;nuser;none")},
	}
}

func replyMarkupKeyboard(keyboard [][]button) map[string]any {
	return map[string]any{"resize_keyboard": true, "keyboard": keyboard}
}

func replyMarkupInline(keyboard [][]button) map[string]any {
	return map[string]any{"inline_keyboard": keyboard}
}

func removeKeyboard() map[string]any {
	return map[string]any{"remove_keyboard": true}
}
