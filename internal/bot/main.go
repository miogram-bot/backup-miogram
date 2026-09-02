package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"miogram/internal/storage"
	"miogram/internal/telegram"
)

type advSearch struct {
	Gender     string   `json:"gender"`
	OnlineTime string   `json:"onlinetime"`
	Age        ageRange `json:"age"`
	State      []string `json:"state"`
	Location   int      `json:"location"`
	Sort       string   `json:"sort,omitempty"`
}

type ageRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

func (s *Service) handleMain(ctx context.Context, c *UpdateContext) (bool, error) {
	if part(c.ExStep, 0) == "province" && part(c.ExStep, 1) == "state" {
		if c.Text == BackButton {
			return true, s.mainMenu(ctx, c, false)
		}
		return true, s.handleProvinceState(ctx, c)
	}
	if part(c.ExData, 0) == "province" {
		return true, s.handleProvinceSearch(ctx, c)
	}
	if strings.HasPrefix(c.Text, "/s") {
		two := strings.TrimPrefix(c.Text, "/s")
		c.ExData = []string{"anon", "", "none"}
		switch two {
		case "r":
			c.ExData[1] = "all"
		case "g":
			c.ExData[1] = "girl"
		case "b":
			c.ExData[1] = "boy"
		}
	}
	if c.Text == "/sr" {
		c.ExData = []string{"anon", "all", "none"}
	}
	if c.Text == "🔗 به یه ناشناس وصلم کن!️" || c.Data == "anon" {
		method := "sendMessage"
		params := map[string]any{
			"chat_id":             c.UserID,
			"text":                "به کی وصلت کنم؟ <code>انتخاب کن👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup":        telegram.JSON(replyMarkupInline(anonymousSearchKeyboard())),
		}
		if c.Data == "anon" {
			method = "editMessageText"
			delete(params, "reply_to_message_id")
			params["message_id"] = c.MessageID
		}
		_, err := s.send(ctx, method, params)
		return true, err
	}
	if part(c.ExStep, 0) == "anon" && part(c.ExStep, 1) == "gps" {
		return true, s.handleAnonGPSInput(ctx, c)
	}
	if part(c.ExData, 0) == "anon" {
		return true, s.handleAnon(ctx, c)
	}
	if c.Text == "📍افراد نزدیک" {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "🛰 چه کسایی رو نشونت بدم؟ <code>انتخاب کن👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("فقط 🙍‍♀️ دختر ها", "findgps;girl;none"), callbackButton("فقط 🙎‍♂️ پسر ها", "findgps;boy;none")},
				{callbackButton("همه رو نشون بده", "findgps;all;none")},
			})),
		})
		return true, err
	}
	if part(c.ExData, 0) == "findgps" {
		return true, s.handleFindGPS(ctx, c)
	}
	if c.Inline != nil {
		return true, s.handleInlineQuery(ctx, c)
	}
	if c.Text == "🔍 جستجوی کاربران 🔎" {
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "چه کسایی رو نشونت بدم؟ <code>انتخاب کن👇</code>",
			"parse_mode":          "HTML",
			"reply_to_message_id": c.MessageID,
			"reply_markup":        telegram.JSON(replyMarkupInline(searchKeyboard())),
		})
		return true, err
	}
	if part(c.ExData, 0) == "search" {
		return true, s.handleSearch(ctx, c)
	}
	if part(c.ExData, 0) == "searchadv" {
		return true, s.handleSearchAdv(ctx, c)
	}
	return false, nil
}

func (s *Service) handleAnonGPSInput(ctx context.Context, c *UpdateContext) error {
	if c.Message == nil || c.Message.Location == nil {
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "⚠️ اطلاعاتی که میفرستی درست نیست !"})
		return err
	}
	_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
	lat := strconv.FormatFloat(c.Message.Location.Latitude, 'f', -1, 64)
	lon := strconv.FormatFloat(c.Message.Location.Longitude, 'f', -1, 64)
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id":    c.UserID,
		"text":       "چه کسی رو از افراد نزدیکت پیدا کنم؟ <code>انتخاب کن👇</code>",
		"parse_mode": "HTML",
		"message_id": c.MessageID,
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{callbackButton("🙎‍♀️ دختر باشه (4💰)", "anon;gps;girl;"+lat+";"+lon), callbackButton("🙍‍♂️ پسر باشه (4💰)", "anon;gps;boy;"+lat+";"+lon)},
			{callbackButton("فرقی نمیکنه (رایگان)", "anon;gps;all;"+lat+";"+lon)},
		})),
	})
	if err == nil {
		_, err = s.checkProfileCoin(ctx, c)
	}
	return err
}

func (s *Service) handleAnon(ctx context.Context, c *UpdateContext) error {
	statusSameAge, sameAge := s.sameAgeStatus(c.User)
	if part(c.ExData, 1) == "gps" {
		return s.handleAnonGPS(ctx, c, statusSameAge, sameAge)
	}
	requestGender := part(c.ExData, 1)
	if requestGender == "" {
		requestGender = "all"
	}
	cost := 0
	if requestGender == "boy" || requestGender == "girl" {
		cost = 2
		if c.User.Balance < cost {
			kb, _ := s.coinKeyboard(ctx)
			_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": fmt.Sprintf("⚠️ توجه: شما سکه کافی ندارید !  (2 سکه مورد نیاز)\n\n<code>💡 برای بدست آوردن سکه میتونی رباتو به دوستات معرفی کنی و به ازای معرفی هر نفر %d .</code>", c.Admin.CoinPerInvite+c.Admin.CoinPerInviteProfile+c.Admin.CoinPerInviteInvite), "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline(kb))})
			return err
		}
	}
	match, err := s.findAnonMatch(ctx, c, requestGender, "normal", sameAge, 0, 0, "")
	if err != nil {
		return err
	}
	if match.UserID == "" {
		if exists, waited := s.ownSearch(ctx, c, requestGender, "normal"); exists {
			return s.answer(ctx, c, "⚠️ خطا: چندبار میزنی؟ دارم جستجو میکنم.\n\n⏳ لطفا "+waitText(waited)+" دیگر صبر کنید.")
		}
		title := "- 🎲 جستجو شانسی 🎲"
		reason := "connectToUser"
		if requestGender == "boy" {
			title = "- جستجو پسر 🙋‍♂️"
			reason = ""
		} else if requestGender == "girl" {
			title = "- جستجو دختر 🙋‍♀️"
			reason = ""
		}
		_, _ = s.send(ctx, "sendMessage", map[string]any{
			"chat_id":    c.UserID,
			"text":       "🔎 درحال جستجوی مخاطب ناشناس شما\n<code>" + title + "</code>\n\n⏳ حداکثر تا 2 دقیقه صبر کنید.\n\n⚙️ جستجوی همسن : " + statusSameAge,
			"parse_mode": "HTML",
		})
		_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE type='search' AND user_id=$1`, c.UserID)
		req := ""
		if requestGender != "all" {
			req = requestGender
		}
		_, err := s.store.DB().Exec(ctx, `INSERT INTO notif (type,user_id,balance,gender,request_gender,age,latitude,longitude,state,reason,content,status,date) VALUES ('search',$1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9,'normal','doing',$10)`, c.UserID, cost, c.User.Gender, req, sameAge, c.User.Latitude, c.User.Longitude, c.User.State, reason, c.Now)
		return err
	}
	return s.connectAnonUsers(ctx, c, match, cost, match.Balance)
}

func (s *Service) handleAnonGPS(ctx context.Context, c *UpdateContext, statusSameAge string, sameAge int) error {
	if part(c.ExData, 2) == "none" {
		_, err := s.send(ctx, "editMessageText", map[string]any{
			"chat_id":    c.UserID,
			"text":       "چه کسی رو از افراد نزدیکت پیدا کنم؟ <code>انتخاب کن👇</code>",
			"parse_mode": "HTML",
			"message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("📍افراد نزدیک من", "anon;gps;near;none")},
				{callbackButton("🙎‍♀️ دختر باشه (4💰)", "anon;gps;girl;none"), callbackButton("🙍‍♂️ پسر باشه (4💰)", "anon;gps;boy;none")},
				{callbackButton("فرقی نمیکنه (رایگان)", "anon;gps;all;none")},
			})),
		})
		return err
	}
	if part(c.ExData, 2) == "near" {
		_ = s.store.UpdateUserStep(ctx, c.UserID, "anon;gps")
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "⚠️ هنگام ارسال موقعیت مکانی مطمعن شوید GPS موبایل شما روشن است.\n\n" +
				"برای جستجوی افراد نزدیکت روی دکمه «📍ارسال موقعیت جی پی اس » کلیک کن! 👇",
			"parse_mode":               "HTML",
			"disable_web_page_preview": true,
			"reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{
				{{"text": "📍ارسال موقعیت جی پی اس", "request_location": true}},
				{textButton(BackButton)},
			})),
		})
		return err
	}
	lat, lon := c.User.Latitude, c.User.Longitude
	if part(c.ExData, 3) != "none" {
		lat = parseFloat(part(c.ExData, 3))
		lon = parseFloat(part(c.ExData, 4))
	}
	requestGender := part(c.ExData, 2)
	cost := 0
	if requestGender == "boy" || requestGender == "girl" {
		cost = 4
		if c.User.Balance < 4 {
			kb, _ := s.coinKeyboard(ctx)
			_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": fmt.Sprintf("⚠️ توجه: شما سکه کافی ندارید !  (4 سکه مورد نیاز)\n\n<code>💡 برای بدست آوردن سکه میتونی رباتو به دوستات معرفی کنی و به ازای معرفی هر نفر %d .</code>", c.Admin.CoinPerInvite+c.Admin.CoinPerInviteProfile+c.Admin.CoinPerInviteInvite), "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline(kb))})
			return err
		}
	}
	match, err := s.findAnonMatch(ctx, c, requestGender, "gps", sameAge, lat, lon, "")
	if err != nil {
		return err
	}
	if match.UserID == "" {
		if exists, waited := s.ownSearch(ctx, c, requestGender, "gps"); exists {
			return s.answer(ctx, c, "⚠️ خطا: چندبار میزنی؟ دارم جستجو میکنم.\n\n⏳ لطفا "+waitText(waited)+" دیگر صبر کنید.")
		}
		title := "- 🛰 جستجوی اطراف (🎲شانسی)"
		reason := "connectToUserGps"
		if requestGender == "boy" {
			title = "- جستجوی اطراف (جستجو پسر 🙋‍♂️)"
			reason = ""
		} else if requestGender == "girl" {
			title = "- جستجوی اطراف (جستجو دختر 🙋‍♀️)"
			reason = ""
		}
		_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "🔎 درحال جستجوی مخاطب ناشناس شما\n<code>" + title + "</code>\n\n⏳ حداکثر تا 2 دقیقه صبر کنید.\n\n⚙️ جستجوی همسن : " + statusSameAge, "parse_mode": "HTML"})
		_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE type='search' AND user_id=$1`, c.UserID)
		req := ""
		if requestGender != "all" {
			req = requestGender
		}
		_, err := s.store.DB().Exec(ctx, `INSERT INTO notif (type,user_id,balance,gender,request_gender,age,latitude,longitude,state,reason,content,status,date) VALUES ('search',$1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9,'gps','doing',$10)`, c.UserID, cost, c.User.Gender, req, sameAge, lat, lon, c.User.State, reason, c.Now)
		return err
	}
	return s.connectAnonUsers(ctx, c, match, cost, match.Balance)
}

func (s *Service) handleProvinceState(ctx context.Context, c *UpdateContext) error {
	if !s.store.StateExists(ctx, c.Text, 0) {
		states, _ := s.store.StateNames(ctx, 0)
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID, "text": "لطفاً استان را از فهرست انتخاب کنید 👇",
			"reply_markup": telegram.JSON(replyMarkupInline(provinceInlineKeyboard(states))),
		})
		return err
	}
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "province;gender;"+url.QueryEscape(c.Text), "start")
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text":    "جنسیت مخاطب در استان «" + c.Text + "» را انتخاب کنید 👇",
		"reply_markup": telegram.JSON(replyMarkupInline([][]button{
			{callbackButton("فقط 🙍‍♀️ دخترها (۳ سکه)", "province;girl"), callbackButton("فقط 🙍‍♂️ پسرها (۳ سکه)", "province;boy")},
			{callbackButton("فرقی نمی‌کند (رایگان)", "province;all")},
		})),
	})
	return err
}

func (s *Service) handleProvinceSearch(ctx context.Context, c *UpdateContext) error {
	if part(c.ExData, 1) == "select" {
		states, _ := s.store.StateNames(ctx, 0)
		_, err := s.send(ctx, "editMessageText", map[string]any{
			"chat_id": c.UserID, "message_id": c.MessageID,
			"text":         "🌐 استان مورد نظر را انتخاب کنید 👇",
			"reply_markup": telegram.JSON(replyMarkupInline(provinceInlineKeyboard(states))),
		})
		return err
	}
	if part(c.ExData, 1) == "state" {
		stateID := parseInt(part(c.ExData, 2))
		states, err := s.store.StateNames(ctx, 0)
		if err != nil {
			return err
		}
		state, ok := stateByID(states, stateID)
		if !ok {
			return s.answer(ctx, c, "استان انتخاب‌شده معتبر نیست.")
		}
		if err := s.store.UpdateUserStepPrev(ctx, c.UserID, "province;gender;"+url.QueryEscape(state.Name), "start"); err != nil {
			return err
		}
		_, err = s.send(ctx, "editMessageText", map[string]any{
			"chat_id": c.UserID, "message_id": c.MessageID,
			"text": "جنسیت مخاطب در استان «" + state.Name + "» را انتخاب کنید 👇",
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{
				{callbackButton("فقط 🙍‍♀️ دخترها (۳ سکه)", "province;girl"), callbackButton("فقط 🙍‍♂️ پسرها (۳ سکه)", "province;boy")},
				{callbackButton("فرقی نمی‌کند (رایگان)", "province;all")},
				{callbackButton("🔙 انتخاب استان دیگر", "province;select")},
			})),
		})
		return err
	}
	requestGender := part(c.ExData, 1)
	if requestGender != "all" && requestGender != "boy" && requestGender != "girl" {
		return s.answer(ctx, c, "انتخاب نامعتبر است.")
	}
	state, _ := url.QueryUnescape(part(c.ExStep, 2))
	if state == "" || !s.store.StateExists(ctx, state, 0) {
		return s.answer(ctx, c, "ابتدا استان را انتخاب کنید.")
	}
	cost := 0
	if requestGender != "all" {
		cost = 3
		if c.User.Balance < cost {
			return s.answer(ctx, c, "برای جستجوی جنسیت تفکیک‌شده بر پایه استان به ۳ سکه نیاز دارید.")
		}
	}
	_, sameAge := s.sameAgeStatus(c.User)
	match, err := s.findAnonMatch(ctx, c, requestGender, "province", sameAge, 0, 0, state)
	if err != nil {
		return err
	}
	if match.UserID != "" {
		return s.connectAnonUsers(ctx, c, match, cost, match.Balance)
	}
	if exists, waited := s.ownSearch(ctx, c, requestGender, "province"); exists {
		return s.answer(ctx, c, "در حال جستجو هستم؛ "+waitText(waited)+" دیگر صبر کنید.")
	}
	req := ""
	if requestGender != "all" {
		req = requestGender
	}
	_, _ = s.send(ctx, "sendMessage", map[string]any{
		"chat_id":      c.UserID,
		"text":         "🔎 جستجو میان کاربران استان «" + state + "» آغاز شد.\n\n⏳ حداکثر تا ۲ دقیقه صبر کنید.",
		"reply_markup": telegram.JSON(removeKeyboard()),
	})
	_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='end' WHERE type='search' AND user_id=$1`, c.UserID)
	_, err = s.store.DB().Exec(ctx, `INSERT INTO notif (type,user_id,balance,gender,request_gender,age,state,reason,content,status,date) VALUES ('search',$1,$2,$3,nullif($4,''),$5,$6,'connectToProvince','province','doing',$7)`, c.UserID, cost, c.User.Gender, req, sameAge, state, c.Now)
	_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
	return err
}

func stateByID(states []storage.State, id int) (storage.State, bool) {
	for _, state := range states {
		if state.ID == id {
			return state, true
		}
	}
	return storage.State{}, false
}

func (s *Service) sameAgeStatus(user storage.User) (string, int) {
	if user.SameAge {
		return "✅ فعال\n- غیر فعال : /off", user.Age
	}
	return "📴 غیر فعال\n- فعال کردن : /on", 0
}

func (s *Service) findAnonMatch(ctx context.Context, c *UpdateContext, requestGender, content string, sameAge int, lat, lon float64, state string) (storage.Notification, error) {
	var n storage.Notification
	args := []any{c.UserID, c.User.Gender}
	where := `user_id<>$1 AND type='search' AND status='doing' AND content=$3 AND (coalesce(request_gender,'')='' OR request_gender=$2)`
	args = append(args, content)
	if sameAge > 0 {
		where += fmt.Sprintf(` AND age=$%d`, len(args)+1)
		args = append(args, sameAge)
	} else {
		where += fmt.Sprintf(` AND (age=0 OR age=$%d)`, len(args)+1)
		args = append(args, c.User.Age)
	}
	if requestGender == "boy" || requestGender == "girl" {
		where += fmt.Sprintf(` AND gender=$%d`, len(args)+1)
		args = append(args, requestGender)
	}
	if content == "province" {
		where += fmt.Sprintf(` AND state=$%d`, len(args)+1)
		args = append(args, state)
	}
	if content == "gps" {
		latDelta, lonDelta := boundingDeltas(lat, 50)
		latArg, lonArg := len(args)+1, len(args)+2
		latMinArg, latMaxArg := len(args)+3, len(args)+4
		lonMinArg, lonMaxArg := len(args)+5, len(args)+6
		where += fmt.Sprintf(` AND latitude<>0 AND latitude BETWEEN $%d AND $%d AND longitude BETWEEN $%d AND $%d AND (6371 * acos(least(1, greatest(-1, cos(radians($%d))*cos(radians(latitude))*cos(radians(longitude)-radians($%d))+sin(radians($%d))*sin(radians(latitude)))))) < 50`, latMinArg, latMaxArg, lonMinArg, lonMaxArg, latArg, lonArg, latArg)
		args = append(args, lat, lon, lat-latDelta, lat+latDelta, lon-lonDelta, lon+lonDelta)
	}
	tx, err := s.store.DB().Begin(ctx)
	if err != nil {
		return n, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	query := `SELECT id,type,coalesce(user_id,''),balance,coalesce(gender,''),coalesce(request_gender,''),age,latitude,longitude,coalesce(user_id_2,''),coalesce(reason,''),content,status,date,0::float8,coalesce(state,'') FROM notif WHERE ` + where + ` ORDER BY id ASC LIMIT 1 FOR UPDATE SKIP LOCKED`
	err = tx.QueryRow(ctx, query, args...).Scan(&n.ID, &n.Type, &n.UserID, &n.Balance, &n.Gender, &n.RequestGender, &n.Age, &n.Latitude, &n.Longitude, &n.UserID2, &n.Reason, &n.Content, &n.Status, &n.Date, &n.Distance, &n.State)
	if err == pgx.ErrNoRows {
		return storage.Notification{}, nil
	}
	if err != nil {
		return n, err
	}
	if _, err = tx.Exec(ctx, `UPDATE notif SET status='connecting' WHERE id=$1`, n.ID); err != nil {
		return n, err
	}
	return n, tx.Commit(ctx)
}

func (s *Service) ownSearch(ctx context.Context, c *UpdateContext, requestGender, content string) (bool, int64) {
	req := ""
	if requestGender == "boy" || requestGender == "girl" {
		req = requestGender
	}
	var date int64
	err := s.store.DB().QueryRow(ctx, `SELECT date FROM notif WHERE type='search' AND user_id=$1 AND coalesce(request_gender,'')=$2 AND content=$3 AND status='doing' ORDER BY id DESC LIMIT 1`, c.UserID, req, content).Scan(&date)
	if err != nil {
		return false, 0
	}
	limit := int64(120)
	return true, limit - (c.Now - date)
}

func (s *Service) connectAnonUsers(ctx context.Context, c *UpdateContext, match storage.Notification, costCurrent, costMatch int) error {
	if err := s.store.CreateChat(ctx, c.UserID, match.UserID, c.Now, costCurrent, costMatch); err != nil {
		_, _ = s.store.DB().Exec(ctx, `UPDATE notif SET status='doing' WHERE id=$1 AND status='connecting'`, match.ID)
		return err
	}
	_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "👀 پیدا کردم وصلتون کرد\n\nبه مخاطبت سلام کن 🗣", "reply_markup": telegram.JSON(replyMarkupKeyboard(chatKeyboard()))})
	_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": match.UserID, "text": "👀 پیدا کردم وصلتون کرد\n\nبه مخاطبت سلام کن 🗣", "reply_markup": telegram.JSON(replyMarkupKeyboard(chatKeyboard()))})
	return err
}

func (s *Service) handleFindGPS(ctx context.Context, c *UpdateContext) error {
	if c.User.Latitude == 0 {
		_, err := s.send(ctx, "editMessageText", map[string]any{"chat_id": c.UserID, "text": "انتظار نداری که بدون دونستن موقعیتت بتونم افراد نزدیکتو پیدا کنم؟\n\n⚠️ خطا: شما موقعیت مکانی خود را ثبت نکرده اید.\n\nبا زدن گزینه 📍 ثبت موقعیت GPS  ، موقعیت خود را ثبت کنید 👇", "message_id": c.MessageID, "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("📍 ثبت موقعیت GPS", "profile;gps")}}))})
		return err
	}
	gender := part(c.ExData, 1)
	page := 1
	if part(c.ExData, 2) != "none" {
		page = parseInt(part(c.ExData, 2))
	}
	step := 20
	users, total, err := s.nearUsers(ctx, c, gender, 20, 259200, page, step)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		if part(c.ExData, 2) == "none" {
			return s.answer(ctx, c, "⚠️ در 3 روز اخیر کاربری در اطراف شما آنلاین نبوده است.")
		}
		return s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
	}
	txt := s.usersText(c, users, (page-1)*step+1)
	return s.listShow(ctx, c, "🛰 لیست افراد نزدیک به شما\n\n"+txt+"\nجستجو شده در "+toEnglish(jdate(s.loc, "Y-m-d H:i", c.Now)), "findgps;"+gender, total, page, step, nil, nil)
}

func (s *Service) handleSearch(ctx context.Context, c *UpdateContext) error {
	if part(c.ExData, 2) == "none" {
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "چه کسایی رو از بین " + SearchStatusButton[part(c.ExData, 1)] + " نشونت بدم؟ <code>انتخاب کن👇</code>", "parse_mode": "HTML", "reply_to_message_id": c.MessageID, "reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("فقط 🙍‍♀️ دختر ها", "search;"+part(c.ExData, 1)+";girl;none"), callbackButton("فقط 🙍‍♂️ پسر ها", "search;"+part(c.ExData, 1)+";boy;none")}, {callbackButton("همه رو نشون بده", "search;"+part(c.ExData, 1)+";all;none")}}))})
		return err
	}
	typ, gender := part(c.ExData, 1), part(c.ExData, 2)
	page := 1
	if part(c.ExData, 3) != "none" {
		page = parseInt(part(c.ExData, 3))
	}
	step := 10
	users, total, err := s.searchUsers(ctx, c, typ, gender, page, step)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		if part(c.ExData, 3) == "none" {
			return s.answer(ctx, c, "⚠️ کاربری یافت نشد.")
		}
		return s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد.")
	}
	txt := s.usersText(c, users, (page-1)*step+1)
	return s.listShow(ctx, c, SearchTitle[typ]+"\n\n"+txt+"\nجستجو شده در "+toEnglish(jdate(s.loc, "Y-m-d H:i", c.Now)), "search;"+typ+";"+gender, total, page, step, nil, nil)
}

func (s *Service) handleSearchAdv(ctx context.Context, c *UpdateContext) error {
	if part(c.ExData, 1) == "none" {
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "🔍 جستجوی پیشرفته🔎\n\n🎌 چه کسایی رو نشونت بدم؟  <code>جنسیت رو انتخاب کن👇</code>", "parse_mode": "HTML", "reply_to_message_id": c.MessageID, "reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("فقط 🙍‍♀️ دختر ها", "searchadv;no;gender;girl;none"), callbackButton("فقط 🙍‍♂️ پسر ها", "searchadv;no;gender;boy;none")}, {callbackButton("همه رو نشون بده", "searchadv;no;gender;all;none")}}))})
		return err
	}
	if part(c.ExData, 2) == "gender" {
		search := advSearch{Gender: part(c.ExData, 3), OnlineTime: "0", State: []string{}, Location: 0}
		var id int64
		raw, _ := json.Marshal(search)
		if err := s.store.DB().QueryRow(ctx, `INSERT INTO search (user_id,search,created_at) VALUES ($1,$2,$3) RETURNING id`, c.UserID, string(raw), c.Now).Scan(&id); err != nil {
			return err
		}
		return s.renderAdvStates(ctx, c, id, search)
	}
	id := parseInt64(part(c.ExData, 1))
	search, err := s.getAdvSearch(ctx, id)
	if err != nil {
		return s.answer(ctx, c, "⚠️ صفحه دیگری وجود ندارد")
	}
	switch part(c.ExData, 2) {
	case "state":
		if part(c.ExData, 3) == "location" {
			search.Location = 1 - search.Location
		} else if part(c.ExData, 3) == "all" {
			if len(search.State) == 31 {
				search.State = []string{}
				search.Location = 0
			} else {
				states, _ := s.store.StateNames(ctx, 0)
				search.State = []string{}
				for _, st := range states {
					search.State = append(search.State, searchStateEncoded(st.Name))
				}
				search.Location = 1
			}
		} else {
			stateID := parseInt(part(c.ExData, 3))
			var name string
			_ = s.store.DB().QueryRow(ctx, `SELECT state FROM states WHERE parent=0 AND id=$1`, stateID).Scan(&name)
			encoded := searchStateEncoded(name)
			search.State = toggleString(search.State, encoded)
		}
		_ = s.saveAdvSearch(ctx, id, search)
		return s.renderAdvStates(ctx, c, id, search)
	case "age":
		if len(search.State) == 0 && search.Location == 0 {
			return nil
		}
		_, err := s.send(ctx, "editMessageText", map[string]any{"chat_id": c.UserID, "text": s.advSummary(search) + "\n👥 بازه سنی : [❓ - ❓]\n\n<code>حداقل سن بازه رو انتخاب کن 👇</code>", "message_id": c.MessageID, "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline(ageInlineKeyboard(fmt.Sprintf("searchadv;%d;end;%%d", id))))})
		return err
	case "end":
		search.Age.Start = parseInt(part(c.ExData, 3))
		_ = s.saveAdvSearch(ctx, id, search)
		_, err := s.send(ctx, "editMessageText", map[string]any{"chat_id": c.UserID, "text": s.advSummary(search) + fmt.Sprintf("\n👥 بازه سنی : [%d - ❓]\n\n<code>حداکثر سن بازه رو انتخاب کن 👇</code>", search.Age.Start), "message_id": c.MessageID, "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline(ageInlineKeyboard(fmt.Sprintf("searchadv;%d;onlinetime;%%d", id))))})
		return err
	case "onlinetime":
		search.Age.End = parseInt(part(c.ExData, 3))
		_ = s.saveAdvSearch(ctx, id, search)
		kb := [][]button{{callbackButton("تا 6 ساعت قبل", fmt.Sprintf("searchadv;%d;sort;21600", id)), callbackButton("تا یک ساعت قبل", fmt.Sprintf("searchadv;%d;sort;3600", id))}, {callbackButton("تا دو روز قبل", fmt.Sprintf("searchadv;%d;sort;172800", id)), callbackButton("تا یک روز قبل", fmt.Sprintf("searchadv;%d;sort;86400", id))}, {callbackButton("تا یک هفته قبل", fmt.Sprintf("searchadv;%d;sort;604800", id)), callbackButton("تا سه روز قبل", fmt.Sprintf("searchadv;%d;sort;259200", id))}, {callbackButton("همه", fmt.Sprintf("searchadv;%d;sort;all", id))}}
		_, err := s.send(ctx, "editMessageText", map[string]any{"chat_id": c.UserID, "text": s.advSummary(search) + fmt.Sprintf("\n👥 بازه سنی : [%d - %d]\n\n👀 آخرین حضور : []\n\n<code>آخرین زمان حضور کاربر رو انتخاب کن 👇</code>", search.Age.Start, search.Age.End), "message_id": c.MessageID, "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline(kb))})
		return err
	case "sort":
		search.OnlineTime = part(c.ExData, 3)
		_ = s.saveAdvSearch(ctx, id, search)
		kb := [][]button{{callbackButton("تاریخ آنلاین", fmt.Sprintf("searchadv;%d;show;last_activity", id)), callbackButton("فاصله نزدیک", fmt.Sprintf("searchadv;%d;show;near", id))}, {callbackButton("کمترین سن", fmt.Sprintf("searchadv;%d;show;min_age", id)), callbackButton("سن نزدیک", fmt.Sprintf("searchadv;%d;show;age", id)), callbackButton("بیشترین سن", fmt.Sprintf("searchadv;%d;show;max_age", id))}, {callbackButton("فقط نمایش بدون چت های آنلاین", fmt.Sprintf("searchadv;%d;show;wchat", id))}}
		_, err := s.send(ctx, "editMessageText", map[string]any{"chat_id": c.UserID, "text": s.advSummary(search) + fmt.Sprintf("\n👥 بازه سنی : [%d - %d]\n\n👀 آخرین حضور : [%s]\n\n<code>اولویت ترتیب نمایش لیست کاربران رو انتخاب کن 👇</code>", search.Age.Start, search.Age.End, OnlineTimeText[search.OnlineTime]), "message_id": c.MessageID, "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline(kb))})
		return err
	case "show":
		search.Sort = part(c.ExData, 3)
		_ = s.saveAdvSearch(ctx, id, search)
		return s.showAdvResults(ctx, c, id, search, 1, true)
	case "result":
		page := parseInt(part(c.ExData, 3))
		return s.showAdvResults(ctx, c, id, search, page, false)
	}
	return nil
}

func (s *Service) handleInlineQuery(ctx context.Context, c *UpdateContext) error {
	query := c.Inline.Query
	offset := parseInt(c.Inline.Offset)
	if offset < 0 {
		offset = 0
	}
	step := 50
	var users []storage.User
	var total int
	var err error
	if strings.HasPrefix(query, "جستجو ") {
		id := parseInt64(strings.TrimPrefix(query, "جستجو "))
		search, err := s.getAdvSearch(ctx, id)
		if err != nil {
			return nil
		}
		users, total, err = s.advUsers(ctx, c, search, offset/step+1, step)
	} else if query == "لیست لایک کننده های پروفایل من" {
		users, total, err = s.inlineLikes(ctx, c, offset, step)
	} else {
		typeUsers := "all"
		inlineEdit := query
		if strings.HasSuffix(query, "خانم") {
			typeUsers = "girl"
			inlineEdit = strings.TrimSpace(strings.TrimSuffix(query, "خانم"))
		} else if strings.HasSuffix(query, "آقا") {
			typeUsers = "boy"
			inlineEdit = strings.TrimSpace(strings.TrimSuffix(query, "آقا"))
		}
		if inlineEdit == "کاربران نزدیک" {
			users, total, err = s.nearUsers(ctx, c, typeUsers, 20, 259200, offset/step+1, step)
		} else {
			typ := ""
			for k, v := range SearchStatus {
				if v == inlineEdit {
					typ = k
					break
				}
			}
			if typ == "" {
				return nil
			}
			users, total, err = s.searchUsers(ctx, c, typ, typeUsers, offset/step+1, step)
		}
	}
	if err != nil || len(users) == 0 {
		return err
	}
	results := s.inlineResults(c, users)
	nextOffset := ""
	if offset+step < total {
		nextOffset = strconv.Itoa(offset + step)
	}
	_, err = s.send(ctx, "answerInlineQuery", map[string]any{"inline_query_id": c.Inline.ID, "cache_time": 0, "results": telegram.JSON(results), "next_offset": nextOffset})
	return err
}

func (s *Service) searchUsers(ctx context.Context, c *UpdateContext, typ, gender string, page, step int) ([]storage.User, int, error) {
	where := []string{"user_id<>$1", "state IS NOT NULL", "is_fake=false"}
	args := []any{c.UserID}
	if gender != "all" && gender != "" {
		where = append(where, fmt.Sprintf("gender=$%d", len(args)+1))
		args = append(args, gender)
	}
	order := "ORDER BY last_activity DESC"
	switch typ {
	case "sage":
		where = append(where, fmt.Sprintf("age=$%d", len(args)+1), fmt.Sprintf("(last_activity+259200)>$%d", len(args)+2))
		args = append(args, c.User.Age, c.Now)
	case "sstate":
		where = append(where, fmt.Sprintf("state=$%d", len(args)+1), fmt.Sprintf("(last_activity+259200)>$%d", len(args)+2))
		args = append(args, c.User.State, c.Now)
	case "wchat":
		where = append(where, "num_chats=0")
	case "nuser":
		where = append(where, fmt.Sprintf("(last_activity+60)>$%d", len(args)+1))
		args = append(args, c.Now)
		order = "ORDER BY created_at DESC"
	}
	return s.queryUsersPage(ctx, where, order, args, page, step)
}

func (s *Service) nearUsers(ctx context.Context, c *UpdateContext, gender string, radiusKM int, onlineWithin int64, page, step int) ([]storage.User, int, error) {
	latDelta, lonDelta := boundingDeltas(c.User.Latitude, radiusKM)
	args := []any{c.UserID, c.User.Latitude, c.Now, onlineWithin, c.User.Longitude, radiusKM, c.User.Latitude - latDelta, c.User.Latitude + latDelta, c.User.Longitude - lonDelta, c.User.Longitude + lonDelta}
	cond := []string{
		"user_id<>$1",
		"is_fake=false",
		"(last_activity+$4)>$3",
		"latitude BETWEEN $7 AND $8",
		"longitude BETWEEN $9 AND $10",
		"(6371 * acos(least(1, greatest(-1, cos(radians($2))*cos(radians(latitude))*cos(radians(longitude)-radians($5))+sin(radians($2))*sin(radians(latitude)))))) < $6",
	}
	if gender != "all" && gender != "" {
		cond = append(cond, fmt.Sprintf("gender=$%d", len(args)+1))
		args = append(args, gender)
	}
	nearOrder := "ORDER BY (6371 * acos(least(1, greatest(-1, cos(radians($2))*cos(radians(latitude))*cos(radians(longitude)-radians($5))+sin(radians($2))*sin(radians(latitude)))))) ASC"
	return s.queryUsersPage(ctx, cond, nearOrder, args, page, step)
}

func (s *Service) queryUsersPage(ctx context.Context, where []string, order string, args []any, page, step int) ([]storage.User, int, error) {
	if page < 1 {
		page = 1
	}
	if step < 1 {
		step = 10
	}
	countQuery := `SELECT count(*) FROM users WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := s.store.DB().QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	argsLimit := append(append([]any{}, args...), step, (page-1)*step)
	query := `SELECT ` + storage.UserColumns + ` FROM users WHERE ` + strings.Join(where, " AND ") + ` ` + order + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	users, err := s.store.QueryUsers(ctx, query, argsLimit...)
	return users, total, err
}

func (s *Service) usersText(c *UpdateContext, users []storage.User, start int) string {
	var b strings.Builder
	for i, user := range users {
		b.WriteString(userInfoLine(c.Now, c.User, user, start+i, ""))
	}
	return b.String()
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func boundingDeltas(lat float64, radiusKM int) (float64, float64) {
	if radiusKM <= 0 {
		radiusKM = 50
	}
	latDelta := float64(radiusKM) / 111.0
	cosLat := math.Cos(lat * math.Pi / 180)
	if math.Abs(cosLat) < 0.01 {
		return latDelta, 180
	}
	return latDelta, latDelta / math.Abs(cosLat)
}

func toggleString(values []string, target string) []string {
	for i, value := range values {
		if value == target {
			return append(values[:i], values[i+1:]...)
		}
	}
	return append(values, target)
}

func (s *Service) getAdvSearch(ctx context.Context, id int64) (advSearch, error) {
	var raw string
	var search advSearch
	if err := s.store.DB().QueryRow(ctx, `SELECT search::text FROM search WHERE id=$1`, id).Scan(&raw); err != nil {
		return search, err
	}
	err := json.Unmarshal([]byte(raw), &search)
	return search, err
}

func (s *Service) saveAdvSearch(ctx context.Context, id int64, search advSearch) error {
	raw, _ := json.Marshal(search)
	_, err := s.store.DB().Exec(ctx, `UPDATE search SET search=$2 WHERE id=$1`, id, string(raw))
	return err
}

func (s *Service) renderAdvStates(ctx context.Context, c *UpdateContext, id int64, search advSearch) error {
	location := "📍افراد نزدیک من"
	if search.Location == 1 {
		location += "✔️"
	}
	kb := [][]button{{callbackButton("➡️ مرحله بعدی", fmt.Sprintf("searchadv;%d;age;none", id))}, {callbackButton("✅ انتخاب همه", fmt.Sprintf("searchadv;%d;state;all", id)), callbackButton(location, fmt.Sprintf("searchadv;%d;state;location", id))}}
	states, _ := s.store.StateNames(ctx, 0)
	row := []button{}
	for _, st := range states {
		label := st.Name
		if containsString(search.State, searchStateEncoded(st.Name)) {
			label += "✔️"
		}
		row = append(row, callbackButton(label, fmt.Sprintf("searchadv;%d;state;%d", id, st.ID)))
		if len(row) == 3 {
			kb = append(kb, row)
			row = []button{}
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}
	kb = append(kb, []button{callbackButton("➡️ مرحله بعدی", fmt.Sprintf("searchadv;%d;age;none", id))})
	_, err := s.send(ctx, "editMessageText", map[string]any{"chat_id": c.UserID, "text": "👫 جنسیت : [" + GenderText[search.Gender] + "]\n\n🎌 استان های انتخاب شده  : [" + statesSelectAdv(search.State, search.Location == 1) + "]\n\n<code>استان های مورد نظرتو انتخاب کن و در آخر گزینه «➡️ مرحله بعدی » رو بزن 👇</code>", "message_id": c.MessageID, "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline(kb))})
	return err
}

func (s *Service) advSummary(search advSearch) string {
	return "👫 جنسیت : [" + GenderText[search.Gender] + "]\n\n🎌 استان های انتخاب شده  : [" + statesSelectAdv(search.State, search.Location == 1) + "]\n\n"
}

func (s *Service) showAdvResults(ctx context.Context, c *UpdateContext, id int64, search advSearch, page int, first bool) error {
	step := 10
	users, total, err := s.advUsers(ctx, c, search, page, step)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		_ = s.answer(ctx, c, "⚠️ کاربری با تنظیمات مربوطه یافت نشد.")
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		return nil
	}
	txt := s.usersText(c, users, (page-1)*step+1)
	if first {
		s.deleteMessage(ctx, c.UserID, c.MessageID)
		resp, _ := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": fmt.Sprintf("♻️ در حال جستجو ی پیشرفته\n\n👫 جنسیت : [%s]\n🎌 استان های انتخاب شده  : [%s]\n👥 بازه سنی : [%d - %d]\n👀 آخرین حضور : [%s]\n🔻 ترتیب نمایش : [%s]\n\n👇👇👇👇👇", GenderText[search.Gender], statesSelectAdv(search.State, search.Location == 1), search.Age.Start, search.Age.End, OnlineTimeText[search.OnlineTime], SortText[search.Sort]), "message_id": c.MessageID, "parse_mode": "HTML"})
		replyTo := 0
		if msg, ok := s.tg.SentMessage(resp); ok {
			replyTo = msg.MessageID
		}
		_, err := s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "نتایج 🔍 جستجوی پیشرفته🔎\n\n" + txt + "\nجستجو شده در " + toEnglish(jdate(s.loc, "Y-m-d H:i", c.Now)), "reply_to_message_id": replyTo, "parse_mode": "HTML", "reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("➡️ مشاهده ادامه لیست", fmt.Sprintf("searchadv;%d;result;2", id))}}))})
		return err
	}
	return s.listShow(ctx, c, "نتایج 🔍 جستجوی پیشرفته🔎\n\n"+txt+"\nجستجو شده در "+toEnglish(jdate(s.loc, "Y-m-d H:i", c.Now)), fmt.Sprintf("searchadv;%d;result", id), total, page, step, nil, nil)
}

func (s *Service) advUsers(ctx context.Context, c *UpdateContext, search advSearch, page, step int) ([]storage.User, int, error) {
	where := []string{"user_id<>$1", "gender IS NOT NULL", "age>=$2 AND age<=$3", "is_fake=false"}
	args := []any{c.UserID, search.Age.Start, search.Age.End}
	if search.Gender != "all" && search.Gender != "" {
		where = append(where, fmt.Sprintf("gender=$%d", len(args)+1))
		args = append(args, search.Gender)
	}
	if search.Sort == "wchat" {
		where = append(where, "num_chats=0")
	}
	if len(search.State) > 0 {
		holders := []string{}
		for _, encoded := range search.State {
			decoded, _ := url.QueryUnescape(encoded)
			args = append(args, decoded)
			holders = append(holders, fmt.Sprintf("$%d", len(args)))
		}
		where = append(where, "state IN ("+strings.Join(holders, ",")+")")
	}
	if search.OnlineTime != "" && search.OnlineTime != "all" && search.OnlineTime != "0" {
		args = append(args, parseInt(search.OnlineTime), c.Now)
		where = append(where, fmt.Sprintf("($%d-last_activity)<$%d", len(args), len(args)-1))
	}
	if search.Location == 1 {
		latDelta, lonDelta := boundingDeltas(c.User.Latitude, 50)
		latArg, lonArg := len(args)+1, len(args)+2
		radiusArg := len(args) + 3
		latMinArg, latMaxArg := len(args)+4, len(args)+5
		lonMinArg, lonMaxArg := len(args)+6, len(args)+7
		args = append(args, c.User.Latitude, c.User.Longitude, 50, c.User.Latitude-latDelta, c.User.Latitude+latDelta, c.User.Longitude-lonDelta, c.User.Longitude+lonDelta)
		where = append(where, fmt.Sprintf("latitude BETWEEN $%d AND $%d", latMinArg, latMaxArg))
		where = append(where, fmt.Sprintf("longitude BETWEEN $%d AND $%d", lonMinArg, lonMaxArg))
		where = append(where, fmt.Sprintf("(6371 * acos(least(1, greatest(-1, cos(radians($%d))*cos(radians(latitude))*cos(radians(longitude)-radians($%d))+sin(radians($%d))*sin(radians(latitude)))))) <= $%d", latArg, lonArg, latArg, radiusArg))
	}
	order := "ORDER BY last_activity DESC"
	switch search.Sort {
	case "near":
		latArg, lonArg := len(args)+1, len(args)+2
		args = append(args, c.User.Latitude, c.User.Longitude)
		where = append(where, fmt.Sprintf("latitude<>0 AND $%d::float8 IS NOT NULL AND $%d::float8 IS NOT NULL", latArg, lonArg))
		order = fmt.Sprintf("ORDER BY (6371 * acos(least(1, greatest(-1, cos(radians($%d))*cos(radians(latitude))*cos(radians(longitude)-radians($%d))+sin(radians($%d))*sin(radians(latitude)))))) ASC", latArg, lonArg, latArg)
	case "min_age":
		order = "ORDER BY age ASC"
	case "age":
		where = append(where, fmt.Sprintf("age<($%d+5) AND age>($%d-5)", len(args)+1, len(args)+1))
		args = append(args, c.User.Age)
	case "max_age":
		order = "ORDER BY age DESC"
	}
	return s.queryUsersPage(ctx, where, order, args, page, step)
}

func (s *Service) inlineResults(c *UpdateContext, users []storage.User) []telegram.InlineQueryResultArticle {
	results := make([]telegram.InlineQueryResultArticle, 0, len(users))
	for _, user := range users {
		statusChat := ""
		if strings.HasPrefix(user.Step, "chatting;") {
			statusChat = " (در حال چت)"
		}
		d := ""
		if c.User.Latitude != 0 && user.Latitude != 0 {
			d = fmt.Sprintf(" (🏁 %.1fkm)", distanceKM(c.User.Latitude, c.User.Longitude, user.Latitude, user.Longitude))
		}
		city := ""
		if user.City != "" {
			city = " (" + user.City + ")"
		}
		name := user.Name
		if name == "" {
			name = "❓"
		}
		desc := fmt.Sprintf("%d %s%s%s\n%s%s\n", user.Age, user.State, city, d, lastActivity(c.Now, user.LastActivity), statusChat)
		thumb := s.fileURL("noimage-" + user.Gender + ".jpg")
		results = append(results, telegram.InlineQueryResultArticle{
			Type:        "article",
			ID:          strconv.FormatInt(user.ID, 10),
			Title:       "‏" + GenderEmoji[user.Gender] + " " + name,
			ThumbURL:    thumb,
			Description: desc,
			ParseMode:   "HTML",
			InputMessageContent: telegram.InputMessageContent{
				ParseMode:             "HTML",
				DisableWebPagePreview: true,
				MessageText:           "/user_" + user.UniqID,
			},
		})
	}
	return results
}

func (s *Service) inlineLikes(ctx context.Context, c *UpdateContext, offset, step int) ([]storage.User, int, error) {
	rows, err := s.store.DB().Query(ctx, `SELECT user_id FROM likes WHERE target_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, c.UserID, step, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var users []storage.User
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		if u, err := s.store.UserByID(ctx, id); err == nil {
			users = append(users, u)
		}
	}
	var total int
	_ = s.store.DB().QueryRow(ctx, `SELECT count(*) FROM likes WHERE target_id=$1`, c.UserID).Scan(&total)
	return users, total, rows.Err()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
