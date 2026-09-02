package bot

const (
	MainButton        = "🏛️ صفحه اصلی"
	BackButton        = "بازگشت 🔙"
	PanelButton       = "👨‍💻 پنل ادمین"
	EndChatButton     = "پایان چت"
	ShowProfileButton = "👀مشاهده پروفایل این مخاطب👤"
	SupportButton     = "📨 پشتیبانی"
	PrivateChatButton = "🔒 تغییر حالت خصوصی"
	TicTacToeButton   = "🎮 بازی دوز"

	adminTopicSupport         = 44
	adminTopicViolationReport = 45
	adminTopicUserFiles       = 49
	adminTopicProfile         = 50
	adminTopicPaymentReceipt  = 84
)

var BotButtons = map[string]bool{
	"🔗 به یه ناشناس وصلم کن!️":       true,
	"🔍 جستجوی کاربران 🔎":             true,
	"📍افراد نزدیک":                   true,
	"💰سکه":                           true,
	"👤پروفایل":                       true,
	"🤔راهنما":                        true,
	"🚸 معرفی به دوستان (سکه رایگان)": true,
	PrivateChatButton:                true,
	TicTacToeButton:                  true,
	MainButton:                       true,
	BackButton:                       true,
	PanelButton:                      true,
	"👤 پنل ادمین 👤":                  true,
}

var GenderText = map[string]string{
	"unknown": "نامشخص",
	"boy":     "پسر",
	"girl":    "دختر",
	"all":     "همه",
}

var PaymentStatusText = map[string]string{
	"move_gateway": "در انتظار پرداخت ⏳",
	"success":      "موفق ✅",
	"failed":       "ناموفق ❗️",
	"expired":      "منقضی شده ⛔️",
	"first_level":  "در انتظار پرداخت ⏳",
	"card_receipt": "در انتظار ارسال فیش ⏳",
	"card_review":  "در انتظار تأیید فیش ⏳",
	"rejected":     "رد شده ❌",
}

var SearchStatus = map[string]string{
	"sage":   "کاربران همسنی",
	"sstate": "کاربران هم استانی",
	"wchat":  "کاربران بدون چت",
	"nuser":  "کاربران جدید",
}

var SearchStatusButton = map[string]string{
	"sage":   "👥 هم سنی ها",
	"sstate": "🎌 هم استانی ها",
	"wchat":  "🚶‍♂️ بدون چت ها 🚶‍♀️",
	"nuser":  "🙋‍♂️ کاربران جدید 🙋‍♀️",
}

var SearchTitle = map[string]string{
	"sage":   "👥 لیست افراد هم سن شما که در 3 روز اخیر آنلاین بوده اند",
	"sstate": "🎌 لیست افراد هم استانی شما که در 3 روز اخیر آنلاین بوده اند",
	"wchat":  "🚶‍♀️لیست کاربران بدون چت آنلاین 🚶‍♂️",
	"nuser":  "🙋‍♀️ لیست کاربران جدید آنلاین🙋‍♂️",
}

var ReportOptions = map[string]string{
	"ads":            "تبلیغات سایت ها، ربات ها و کانال ها",
	"immoral":        "ارسال محتوای غیر اخلاقی",
	"disturb":        "ایجاد مزاحمت",
	"dissemination":  "پخش شماره موبایل یا اطلاعات شخصی دیگر",
	"immoralprofile": "کلمات یا عکس غیر اخلاقی و یا توهین آمیز در پروفایل",
	"wronggender":    "جنسیت اشتباه در پروفایل",
	"other":          "دیگر موارد..",
}

var OnlineTimeText = map[string]string{
	"all":    "همه",
	"3600":   "تا یک ساعت قبل",
	"21600":  "تا 6 ساعت قبل",
	"86400":  "تا یک روز قبل",
	"172800": "تا دو روز قبل",
	"259200": "تا سه روز قبل",
	"604800": "تا یک هفته قبل",
}

var SortText = map[string]string{
	"last_activity": "تاریخ آنلاین",
	"near":          "فاصله نزدیک",
	"min_age":       "کمترین سن",
	"age":           "سن نزدیک",
	"max_age":       "بیشترین سن",
	"wchat":         "فقط نمایش بدون چت های آنلاین",
}

var GenderWithEmoji = map[string]string{
	"boy":  "🙎‍♂️ پسر",
	"girl": "🙎‍♀️ دختر",
}

var GenderEmoji = map[string]string{
	"boy":  "🙎‍♂️",
	"girl": "🙎‍♀️",
	"":     "❓",
}

func mainMenuText() string {
	return "خب ، حالا چه کاری برات انجام بدم؟\n\n" +
		"<code>از منوی پایین👇 انتخاب کن</code>"
}
