package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"miogram/internal/fleet"
	"miogram/internal/telegram"
)

const (
	adsWelcomeBonus        = 5
	registrationSearchText = "همین الان روی «🎲 جستجوی شانسی 🎲» رو بزن و شانستو #رایگان و بدون نیاز به سکه امتحان کن 😎👇\n\nچه کسایی رو نشونت بدم؟ انتخاب کن👇"
)

func (s *Service) handleAuth(ctx context.Context, c *UpdateContext) (handled bool, stop bool, err error) {
	// Classroom-selection deep link: /start classroom_<TelegramUserID>.
	if strings.HasPrefix(c.Text, "/start classroom_") {
		if c.User.UserID == "" {
			if _, cerr := s.store.CreateUser(ctx, c.UserID, "", c.Now); cerr != nil {
				return false, true, cerr
			}
		}
		return true, false, s.handleClassroomChoice(ctx, c)
	}

	// بارگذاری یا ایجاد کاربر برای همه درخواست‌ها
	var isNew bool
	if c.UserID != "" {
		user, err := s.store.UserByID(ctx, c.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, cerr := s.store.CreateUser(ctx, c.UserID, "", c.Now); cerr != nil {
				return false, true, cerr
			}
			isNew = true
			user, err = s.store.UserByID(ctx, c.UserID)
			if err != nil {
				return false, true, err
			}
		} else if err != nil {
			return false, true, err
		}
		c.User = user
		c.refreshStep()
	} else {
		if c.Inline != nil {
			return true, true, nil
		}
		return false, true, nil
	}

	if handled, err := s.maybeRedirectNewUser(ctx, c); handled || err != nil {
		return handled, true, err
	}

	if c.Inline != nil {
		return false, false, nil
	}

	// پردازش رفرال و ADS برای کاربران جدید و فقط در پیام /start
	if isNew && c.Message != nil && strings.HasPrefix(c.Text, "/start") {
		adsStart := strings.EqualFold(strings.TrimSpace(c.Text), "/start ADS")
		referral := ""
		if part(c.ExText, 0) == "/start r" {
			ref, err := s.store.UserByUniqOrID(ctx, part(c.ExText, 1))
			if err == nil {
				referral = ref.UserID
			}
		}

		// پردازش رفرال
		if referral != "" {
			_, _ = s.store.DB().Exec(ctx, `UPDATE users SET referral=$2 WHERE user_id=$1`, c.UserID, referral)

			_ = s.store.AddBalance(ctx, referral, c.Admin.CoinPerInvite)
			refUser, _ := s.store.UserByID(ctx, referral)
			if refUser.UserID != "" {
				_, _ = s.send(ctx, "sendMessage", map[string]any{
					"chat_id": refUser.UserID,
					"text": fmt.Sprintf("💥تبریک!\n\nهم اکنون یک نفر با لینک مخصوص شما در ربات عضو شد و به شما %d سکه بابت این معرفی تعلق گرفت.\n\n💰سکه فعلی شما : %d\nبه محض تکمیل پروفایل کاربر %d سکه و به محض معرفی کردن به دیگران %d سکه به شما تعلق خواهد گرفت😎",
						c.Admin.CoinPerInvite, refUser.Balance+c.Admin.CoinPerInvite,
						c.Admin.CoinPerInviteProfile, c.Admin.CoinPerInviteInvite),
					"reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("👥 معرفی افراد بیشتر", "invite")}})),
				})
			}
			if refUser.Referral != "" {
				_ = s.store.AddBalance(ctx, refUser.Referral, c.Admin.CoinPerInviteInvite)
				parent, _ := s.store.UserByID(ctx, refUser.Referral)
				if parent.UserID != "" {
					_, _ = s.send(ctx, "sendMessage", map[string]any{
						"chat_id": parent.UserID,
						"text": fmt.Sprintf("🔔 تبریک ! شما %d سکه بابت معرفی به دیگرانِ کاربری که توسط شما معرفی شده بود دریافت کردید.\n\n💰سکه فعلی شما : %d",
							c.Admin.CoinPerInviteInvite, parent.Balance+c.Admin.CoinPerInviteInvite),
						"reply_markup": telegram.JSON(replyMarkupInline([][]button{{callbackButton("👥 معرفی افراد بیشتر", "invite")}})),
					})
				}
			}
		}

		if adsStart {
			// پردازش ADS: اضافه کردن سکه تبلیغاتی
			if _, err := s.store.DB().Exec(ctx, `UPDATE users SET balance=balance+$2, ads_referral=true, ads_registration_started=true WHERE user_id=$1 AND ads_referral=false`, c.UserID, adsWelcomeBonus); err != nil {
				return false, true, err
			}
			// ارسال پیام تبریک ADS
			_, sendErr := s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text":    fmt.Sprintf("هورااا\nشما %d سکه رایگان دریافت کردید 🥳\n\nحالا بیا ثبت‌نام رو کامل کن👇", adsWelcomeBonus),
			})
			if sendErr != nil {
				return false, true, sendErr
			}
			// ارسال پیام خوش‌آمدگویی معمولی (با دکمه‌های جنسیت)
			_, sendErr = s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text": "سلام " + c.FirstName + " عزیز ✋️\n\n" +
					"به 《" + s.cfg.BotName + " 🤖》 خوش اومدی ، توی این ربات می تونی افراد #نزدیک ات رو پیدا کنی و باهاشون آشنا شی و یا به یه نفر بصورت #ناشناس وصل شی و باهاش #چت کنی ❗️\n\n" +
					"- استفاده از این ربات رایگانه و اطلاعات تلگرام شما مثل اسم،عکس پروفایل یا موقعیت GPS کاملا محرمانه هست😎\n" +
					"<code>برای شروع جنسیتت رو انتخاب کن</code> 👇",
				"parse_mode": "HTML",
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{{
					callbackButton("من🙍‍♂️پسرم", "set_gender;boy"),
					callbackButton("من🙎‍♀️دخترم", "set_gender;girl"),
				}})),
			})
			if sendErr != nil {
				return false, true, sendErr
			}
			// متوقف کردن تابع برای جلوگیری از ورود به handleOnboarding
			return true, true, nil
		} else {
			// استارت معمولی: ارسال پیام خوش‌آمدگویی
			_, sendErr := s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text": "سلام " + c.FirstName + " عزیز ✋️\n\n" +
					"به 《" + s.cfg.BotName + " 🤖》 خوش اومدی ، توی این ربات می تونی افراد #نزدیک ات رو پیدا کنی و باهاشون آشنا شی و یا به یه نفر بصورت #ناشناس وصل شی و باهاش #چت کنی ❗️\n\n" +
					"- استفاده از این ربات رایگانه و اطلاعات تلگرام شما مثل اسم،عکس پروفایل یا موقعیت GPS کاملا محرمانه هست😎\n" +
					"<code>برای شروع جنسیتت رو انتخاب کن</code> 👇",
				"parse_mode": "HTML",
				"reply_markup": telegram.JSON(replyMarkupInline([][]button{{
					callbackButton("من🙍‍♂️پسرم", "set_gender;boy"),
					callbackButton("من🙎‍♀️دخترم", "set_gender;girl"),
				}})),
			})
			if sendErr != nil {
				return false, true, sendErr
			}
			return true, true, nil
		}
	}

	// ادامه برای کاربران موجود (یا ADS که به اینجا نمی‌رسد)
	incompleteProfile := c.User.Gender == "" || c.User.Age == 0 || c.User.State == ""
	if incompleteProfile {
		return s.handleOnboarding(ctx, c)
	}

	if c.UserID != s.cfg.AdminID && c.User.Status == "block" && c.Text != SupportButton && c.Text != "/support" && part(c.ExStep, 0) != "support" {
		_, _ = s.send(ctx, "sendMessage", map[string]any{"chat_id": c.UserID, "text": "⛔️ حساب شما مسدود است. برای پیگیری از گزینه «📨 ارسال پیام به پشتیبانی» استفاده کنید.", "reply_markup": telegram.JSON(replyMarkupKeyboard([][]button{{textButton(SupportButton)}}))})
		return true, true, nil
	}

	if part(c.ExText, 0) == "/start r" || c.Text == MainButton || c.Text == BackButton || c.Data == "start" {
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
			return true, true, err
		}
		return true, true, s.mainMenu(ctx, c, true)
	}

	if c.Data == "checkJoin" {
		return s.handleCheckJoin(ctx, c)
	}
	if handled, err := s.enforceJoin(ctx, c); handled || err != nil {
		return handled, handled, err
	}

	if part(c.ExText, 0) == "/user" && part(c.ExText, 1) == c.User.UniqID {
		c.Text = "👤پروفایل"
		c.ExText = nil
	}
	if c.Text == ShowProfileButton {
		chat, err := s.store.ActiveChat(ctx, c.UserID)
		if err == nil {
			otherID := chat.UserID2
			if otherID == c.UserID {
				otherID = chat.UserID1
			}
			other, err := s.store.UserByID(ctx, otherID)
			if err == nil {
				c.ExText = []string{"/user", other.UniqID}
			}
		}
	}
	if part(c.ExText, 0) == "/user" && part(c.ExText, 1) != "" {
		user, err := s.store.UserByUniq(ctx, part(c.ExText, 1), &c.User)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text":    "⚠️ خطا: کاربر یافت نشد",
			})
			return true, true, err
		}
		if err != nil {
			return false, true, err
		}
		return true, true, s.showUserProfile(ctx, c, user, false)
	}

	return false, false, nil
}

func (s *Service) handleOnboarding(ctx context.Context, c *UpdateContext) (bool, bool, error) {
	// Step 1: Gender selection
	if c.User.Gender == "" {
		if part(c.ExData, 0) == "set_gender" && (part(c.ExData, 1) == "boy" || part(c.ExData, 1) == "girl") {
			_, err := s.store.DB().Exec(ctx, `UPDATE users SET gender=$2 WHERE user_id=$1`, c.UserID, part(c.ExData, 1))
			if err != nil {
				return false, true, err
			}
			_ = s.reloadUser(ctx, c)
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text": "خب حالا سنت رو بهم بگو ؟:\n\n" +
					"<code>• سنت رو از لیست پایین 👇انتخاب کن یا خودت تایپ کن</code>",
				"parse_mode":   "HTML",
				"reply_markup": telegram.JSON(replyMarkupKeyboard(ageKeyboard())),
			})
			return true, true, err
		}
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "⚠️ خطا: فقط یکی از گزینه های زیر را انتخاب کنید 👇",
			"reply_to_message_id": c.MessageID,
			"reply_markup": telegram.JSON(replyMarkupInline([][]button{{
				callbackButton("من🙍‍♂️پسرم", "set_gender;boy"),
				callbackButton("من🙎‍♀️دخترم", "set_gender;girl"),
			}})),
		})
		return true, true, err
	}

	// Step 2: Age selection
	if c.User.Age == 0 {
		age := parseInt(c.Text)
		if age >= 9 && age <= 99 {
			_, err := s.store.DB().Exec(ctx, `UPDATE users SET age=$2 WHERE user_id=$1`, c.UserID, age)
			if err != nil {
				return false, true, err
			}
			_ = s.reloadUser(ctx, c)
			states, _ := s.store.StateNames(ctx, 0)
			_, err = s.send(ctx, "sendMessage", map[string]any{
				"chat_id": c.UserID,
				"text": "خب حالا فقط کافیه استانت رو انتخاب کنی تا وارد ربات شیم\n\n" +
					"<code>• استانت رو از لیست پایین 👇انتخاب کن</code>",
				"parse_mode":   "HTML",
				"reply_markup": telegram.JSON(replyMarkupKeyboard(statesReplyKeyboard(states, false))),
			})
			return true, true, err
		}
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "⚠️ خطا: فقط یکی از گزینه های زیر را انتخاب کنید 👇",
			"reply_to_message_id": c.MessageID,
			"reply_markup":        telegram.JSON(replyMarkupKeyboard(ageKeyboard())),
		})
		return true, true, err
	}

	// Step 3: State selection
	if c.User.State == "" {
		if s.store.StateExists(ctx, c.Text, 0) {
			if _, err := s.store.DB().Exec(ctx, `UPDATE users SET state=$2 WHERE user_id=$1`, c.UserID, c.Text); err != nil {
				return false, true, err
			}
			_ = s.reloadUser(ctx, c)

			// PEAK mode: registration is complete. Send ONLY the migration
			// message. The original message that triggered registration is
			// IGNORED (not processed, not stored, no response).
			if s.fleet != nil && s.fleet.Mode() == fleet.ModePeak && s.cfg.BotID == s.cfg.MainBotID {
				if err := s.sendMigrationToHelper(ctx, c); err != nil {
					return false, true, err
				}
				return true, true, nil
			}

			_, _ = s.send(ctx, "sendMessage", map[string]any{
				"chat_id":             c.UserID,
				"text":                "✅ اطلاعات شما ثبت شد.\n\nبه خانواده بزرگ #" + s.cfg.BotName + " خوش اومدی بهت توصیه میکنم اول از همه با لمس کردن 《🤔 راهنما》 با ربات آشنا شی\n\n<code>از منوی پایین👇 انتخاب کن</code>",
				"parse_mode":          "HTML",
				"reply_to_message_id": c.MessageID,
				"reply_markup":        telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
			})

			_, err := s.send(ctx, "sendMessage", map[string]any{
				"chat_id":      c.UserID,
				"text":         registrationSearchText,
				"parse_mode":   "HTML",
				"reply_markup": telegram.JSON(replyMarkupInline(registrationSearchKeyboard())),
			})

			_ = s.store.UpdateUserStepPrev(ctx, c.UserID, "start", "start")
			return true, true, err
		}
		states, _ := s.store.StateNames(ctx, 0)
		_, err := s.send(ctx, "sendMessage", map[string]any{
			"chat_id":             c.UserID,
			"text":                "استانت رو از لیست پایین انتخاب کن👇",
			"reply_to_message_id": c.MessageID,
			"reply_markup":        telegram.JSON(replyMarkupKeyboard(statesReplyKeyboard(states, false))),
		})
		return true, true, err
	}

	return false, false, nil
}

func (s *Service) handleCheckJoin(ctx context.Context, c *UpdateContext) (bool, bool, error) {
	output := s.notJoinedChannels(ctx, c)
	if output != "" {
		return true, true, s.answer(ctx, c, "⚠️ شما هنوز عضو کانال های « "+output+" » نشده اید، برای عضو شدن در کانال ها JOIN/عضو شدن را در کانال بزنید.")
	}
	s.deleteMessage(ctx, c.UserID, c.MessageID)
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": "✅ عضویت شما تایید شد ! شما هم اکنون می توانید از امکانات ویژه ربات استفاده کنید !\n\n" +
			"<code>یکی از گزینه های زیر را لمس کنید 👇</code>",
		"parse_mode":   "HTML",
		"reply_markup": telegram.JSON(replyMarkupKeyboard(mainMenuKeyboard(s.isAdmin(c)))),
	})
	_ = s.store.UpdateUserStep(ctx, c.UserID, "start")
	return true, true, err
}

func (s *Service) enforceJoin(ctx context.Context, c *UpdateContext) (bool, error) {
	if c.Admin.Channel1 == "" && c.Admin.Channel2 == "" && c.Admin.Channel3 == "" {
		return false, nil
	}
	if part(c.ExStep, 0) != "no_join_channels" && c.User.LastCheckJoinAt+120 >= c.Now {
		return false, nil
	}
	output := ""
	ch1, ch2, ch3 := "", "", ""
	kb := [][]button{}
	if c.Admin.Channel1 != "" && s.isMember(ctx, "@"+c.Admin.Channel1, c.UserID) == "left" {
		kb = append(kb, []button{urlButton("کانال اول ("+c.Admin.Channel1Name+")", "https://t.me/"+c.Admin.Channel1)})
		output = c.Admin.Channel1Name
		ch1 = "👉 @" + c.Admin.Channel1 + "\n"
	}
	if c.Admin.Channel2 != "" && s.isMember(ctx, "@"+c.Admin.Channel2, c.UserID) == "left" {
		kb = append(kb, []button{urlButton("کانال دوم ("+c.Admin.Channel2Name+")", "https://t.me/"+c.Admin.Channel2)})
		if output == "" {
			output = c.Admin.Channel2Name
		} else {
			output += " و " + c.Admin.Channel2Name
		}
		ch2 = "👉 @" + c.Admin.Channel2 + "\n"
	}
	if c.Admin.Channel3 != "" && s.isMember(ctx, "@"+c.Admin.Channel3, c.UserID) == "left" {
		kb = append(kb, []button{urlButton("کانال سوم ("+c.Admin.Channel3Name+")", "https://t.me/"+c.Admin.Channel3)})
		if output == "" {
			output = c.Admin.Channel3Name
		} else {
			output += " و " + c.Admin.Channel3Name
		}
		ch3 = "👉 @" + c.Admin.Channel3 + "\n"
	}
	if output == "" {
		_, err := s.store.DB().Exec(ctx, `UPDATE users SET last_check_join_at=$2 WHERE user_id=$1`, c.UserID, c.Now)
		return false, err
	}
	_, _ = s.store.DB().Exec(ctx, `UPDATE users SET step='no_join_channels;none',last_check_join_at=$2 WHERE user_id=$1`, c.UserID, c.Now)
	kb = append(kb, []button{callbackButton("♻️ بررسی عضویت و فعالسازی ♻️", "checkJoin")})
	_, err := s.send(ctx, "sendMessage", map[string]any{
		"chat_id": c.UserID,
		"text": "سلام " + c.FirstName + " عزیز\n" +
			"برای استفاده از ربات  ابتدا باید در کانال های زیر عضو بشی 👇\n\n" +
			ch1 + ch2 + ch3 + "\n\n" +
			"<code>⚠️ توجه: درصورتی که در عضویت کانال با خطا مواجه میشوید از نسخه اصلی تلگرام استفاده کنید.</code>\n\n" +
			"بعد از عضـــویت « بررسی عضویت و فعال سازی » را لمس کنید تا ربات برای شما فعال شود. 👇\n",
		"parse_mode":   "HTML",
		"reply_markup": telegram.JSON(replyMarkupInline(kb)),
	})
	var exists bool
	_ = s.store.DB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notif WHERE user_id=$1 AND reason='first_msg_force_join' AND status='end')`, c.UserID).Scan(&exists)
	if !exists {
		_, _ = s.store.DB().Exec(ctx, `INSERT INTO notif (user_id,reason,status,date) VALUES ($1,'first_msg_force_join','end',$2)`, c.UserID, c.Now)
		_, _ = s.send(ctx, "sendMessage", map[string]any{
			"chat_id": c.UserID,
			"text": "اسپانسر این ربات کانال های 《 " + output + " 》 هستند.\n\n" +
				"- بعد از عضو شدن توی کانال ها می تونی از ربات استفاده کنی 😍",
		})
	}
	return true, err
}

func (s *Service) notJoinedChannels(ctx context.Context, c *UpdateContext) string {
	parts := []string{}
	if c.Admin.Channel1 != "" && s.isMember(ctx, "@"+c.Admin.Channel1, c.UserID) == "left" {
		parts = append(parts, c.Admin.Channel1Name)
	}
	if c.Admin.Channel2 != "" && s.isMember(ctx, "@"+c.Admin.Channel2, c.UserID) == "left" {
		parts = append(parts, c.Admin.Channel2Name)
	}
	if c.Admin.Channel3 != "" && s.isMember(ctx, "@"+c.Admin.Channel3, c.UserID) == "left" {
		parts = append(parts, c.Admin.Channel3Name)
	}
	return strings.Join(parts, " و ")
}
