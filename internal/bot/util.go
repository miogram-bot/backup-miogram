package bot

import (
	cryptorand "crypto/rand"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"miogram/internal/storage"
	"miogram/internal/telegram"
)

func part(parts []string, idx int) string {
	if idx < 0 || idx >= len(parts) {
		return ""
	}
	return parts[idx]
}

func toPersian(s string) string {
	repl := strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹")
	return repl.Replace(s)
}

func toEnglish(s string) string {
	repl := strings.NewReplacer("۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9")
	return repl.Replace(s)
}

func checkInout(value string) string {
	if strings.TrimSpace(value) == "" {
		return "❓"
	}
	return value
}

func lastActivity(now, activity int64) string {
	switch {
	case activity+60 > now:
		return "هم اکنون 👀 آنلایـــن"
	case activity < now-60 && activity > now-3600:
		return "⏳ " + strconv.FormatInt(int64(math.Ceil(float64(now-activity)/60)), 10) + " دقیقه قبل آنلاین بوده"
	case activity < now-3600 && activity > now-86400:
		return "⏳ " + strconv.FormatInt(int64(math.Ceil(float64(now-activity)/3600)), 10) + " ساعت قبل آنلاین بوده"
	case activity < now-86400:
		return "⏳ " + strconv.FormatInt(int64(math.Ceil(float64(now-activity)/86400)), 10) + " روز قبل آنلاین بوده"
	default:
		return ""
	}
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(toEnglish(strings.TrimSpace(s)))
	return n
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(toEnglish(strings.TrimSpace(s)), 10, 64)
	return n
}

func formatNumber(n int) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}

func validPersianName(s string) bool {
	if len([]rune(s)) > 100 {
		return false
	}
	return regexp.MustCompile(`^[ آابپتثجچحخدذرزژسشصضطظعغفقکگلمنوهیئ\s]+$`).MatchString(s)
}

func userInfoLine(now int64, viewer storage.User, user storage.User, i int, friendName string) string {
	statusChat := ""
	if strings.HasPrefix(user.Step, "chatting;") {
		statusChat = " (در حال چت)"
	}
	d := ""
	if viewer.Latitude != 0 && user.Latitude != 0 {
		d = fmt.Sprintf(" (🏁 %.1fkm)", distanceKM(viewer.Latitude, viewer.Longitude, user.Latitude, user.Longitude))
	}
	city := ""
	if user.City != "" {
		city = " (" + user.City + ")"
	}
	name := user.Name
	if name == "" {
		name = "❓"
	}
	label := ""
	if friendName != "" {
		label = " (" + friendName + ")"
	}
	return fmt.Sprintf("‏%d. %s %s%s /user_%s\n<code>%d %s%s</code>%s\n<code>%s%s</code>\n〰️〰️〰️〰️〰️〰️〰️〰️〰️〰️〰️\n",
		i, GenderEmoji[user.Gender], name, label, user.UniqID, user.Age, user.State, city, d, lastActivity(now, user.LastActivity), statusChat)
}

func distanceKM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func statesSelectAdv(values []string, location bool) string {
	out := ""
	if location {
		out = "📍افراد نزدیک من، "
	}
	for _, value := range values {
		decoded, err := url.QueryUnescape(value)
		if err == nil {
			value = decoded
		}
		out += value + "، "
	}
	return strings.TrimSuffix(out, "، ")
}

func photoID(m *telegram.Message) string {
	if m == nil || len(m.Photo) == 0 {
		return ""
	}
	return m.Photo[len(m.Photo)-1].FileID
}

func fileIDByType(m *telegram.Message, typ string) string {
	if m == nil {
		return ""
	}
	switch typ {
	case "photo":
		return photoID(m)
	case "audio":
		if m.Audio != nil {
			return m.Audio.FileID
		}
	case "video":
		if m.Video != nil {
			return m.Video.FileID
		}
	case "voice":
		if m.Voice != nil {
			return m.Voice.FileID
		}
	case "document":
		if m.Document != nil {
			return m.Document.FileID
		}
	case "animation":
		if m.Animation != nil {
			return m.Animation.FileID
		}
	case "sticker":
		if m.Sticker != nil {
			return m.Sticker.FileID
		}
	}
	return ""
}

func jdate(loc *time.Location, phpLayout string, unix int64) string {
	if unix == 0 {
		unix = time.Now().Unix()
	}
	t := time.Unix(unix, 0).In(loc)
	jy, jm, jd := gregorianToJalali(t.Year(), int(t.Month()), t.Day())
	repl := map[string]string{
		"Y": fmt.Sprintf("%04d", jy),
		"m": fmt.Sprintf("%02d", jm),
		"d": fmt.Sprintf("%02d", jd),
		"H": fmt.Sprintf("%02d", t.Hour()),
		"i": fmt.Sprintf("%02d", t.Minute()),
		"s": fmt.Sprintf("%02d", t.Second()),
	}
	out := phpLayout
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

func gregorianToJalali(gy, gm, gd int) (int, int, int) {
	gdm := []int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	gy2 := gy
	if gm > 2 {
		gy2++
	}
	days := 355666 + 365*gy + (gy2+3)/4 - (gy2+99)/100 + (gy2+399)/400 + gd + gdm[gm-1]
	jy := -1595 + 33*(days/12053)
	days %= 12053
	jy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		jy += (days - 1) / 365
		days = (days - 1) % 365
	}
	var jm, jd int
	if days < 186 {
		jm = 1 + days/31
		jd = 1 + days%31
	} else {
		jm = 7 + (days-186)/30
		jd = 1 + (days-186)%30
	}
	return jy, jm, jd
}

func localAsset(filesDir, name string) telegram.LocalFile {
	return telegram.LocalFile{Path: filepath.Join(filesDir, name)}
}

// generateRandom5Digit generates a random 5-digit number for tracking
func generateRandom5Digit() int64 {
	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(90000))
	if err != nil {
		return 10000 + time.Now().UnixNano()%90000
	}
	return n.Int64() + 10000
}
