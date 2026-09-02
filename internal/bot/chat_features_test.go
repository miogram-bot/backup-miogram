package bot

import (
	"strconv"
	"strings"
	"testing"

	"miogram/internal/storage"
)

func TestTicTacToeWinner(t *testing.T) {
	tests := []struct {
		board string
		want  byte
	}{
		{"XXXOO....", 'X'},
		{"XO.XO..O.", 'O'},
		{"XOOOX.X.X", 'X'},
		{"XOXOXOOXO", 0},
		{"bad", 0},
	}
	for _, test := range tests {
		if got := ticTacToeWinner(test.board); got != test.want {
			t.Errorf("ticTacToeWinner(%q) = %q, want %q", test.board, got, test.want)
		}
	}
}

func TestTicTacToeKeyboardEncodesGameAndCell(t *testing.T) {
	keyboard := ticTacToeKeyboard(42, "X.O......")
	if len(keyboard) != 3 || len(keyboard[0]) != 3 {
		t.Fatalf("unexpected board dimensions: %#v", keyboard)
	}
	if got := keyboard[0][1]["callback_data"]; got != "ttt;42;1" {
		t.Fatalf("callback_data = %v, want ttt;42;1", got)
	}
	if got := keyboard[0][0]["text"]; got != "❌" {
		t.Fatalf("first cell = %v, want X marker", got)
	}
}

func TestTicTacToeKeyboardsKeepGamesIndependent(t *testing.T) {
	first := ticTacToeKeyboard(101, ".........")
	second := ticTacToeKeyboard(202, ".........")
	if first[2][2]["callback_data"] != "ttt;101;8" {
		t.Fatalf("first game callback = %v", first[2][2]["callback_data"])
	}
	if second[2][2]["callback_data"] != "ttt;202;8" {
		t.Fatalf("second game callback = %v", second[2][2]["callback_data"])
	}
}

func TestAdminTopicAssignments(t *testing.T) {
	if adminTopicProfile != 50 || adminTopicViolationReport != 45 || adminTopicPaymentReceipt != 84 || adminTopicSupport != 44 || adminTopicUserFiles != 49 {
		t.Fatalf("unexpected admin topic mapping: profile=%d reports=%d payments=%d support=%d files=%d", adminTopicProfile, adminTopicViolationReport, adminTopicPaymentReceipt, adminTopicSupport, adminTopicUserFiles)
	}
}

func TestEndChatTextIncludesDeletionAndRefund(t *testing.T) {
	text := endChatText("abc123", true, 3)
	for _, want := range []string{"/delete_messages_abc123", "توسط کاربر مقابل", "3 سکه"} {
		if !strings.Contains(text, want) {
			t.Errorf("end text does not contain %q: %s", want, text)
		}
	}
}

func TestGenerateRandom5Digit(t *testing.T) {
	for i := 0; i < 100; i++ {
		got := generateRandom5Digit()
		if got < 10000 || got > 99999 {
			t.Fatalf("tracking number %d is outside five-digit range", got)
		}
	}
}

func TestProvinceSearchIsOnlyInAnonymousMenu(t *testing.T) {
	if keyboardHasCallback(searchKeyboard(), "province;select") {
		t.Fatal("advanced user search must not contain province search")
	}
	if !keyboardHasCallback(anonymousSearchKeyboard(), "province;select") {
		t.Fatal("anonymous connection menu must contain province search")
	}
	if keyboardHasCallback(registrationSearchKeyboard(), "province;select") {
		t.Fatal("post-registration quick search must not contain province search")
	}
}

func TestProvinceKeyboardUsesInlineCallbacks(t *testing.T) {
	states := []storage.State{{ID: 1, Name: "تهران"}, {ID: 2, Name: "فارس"}, {ID: 3, Name: "گیلان"}}
	keyboard := provinceInlineKeyboard(states)
	for _, state := range states {
		want := "province;state;" + strconv.Itoa(state.ID)
		if !keyboardHasCallback(keyboard, want) {
			t.Fatalf("missing inline callback %q", want)
		}
	}
	if !keyboardHasCallback(keyboard, "anon") {
		t.Fatal("province keyboard must include an inline back button")
	}
}

func TestStateByID(t *testing.T) {
	states := []storage.State{{ID: 7, Name: "تهران"}, {ID: 19, Name: "گیلان"}}
	state, ok := stateByID(states, 19)
	if !ok || state.Name != "گیلان" {
		t.Fatalf("stateByID returned %#v, %v", state, ok)
	}
	if _, ok := stateByID(states, 99); ok {
		t.Fatal("stateByID accepted an unknown state")
	}
}

func TestADSUsersReceiveFiveCoinsAndNormalRegistrationPrompt(t *testing.T) {
	if adsWelcomeBonus != 5 {
		t.Fatalf("ADS bonus = %d, want 5", adsWelcomeBonus)
	}
	if !strings.Contains(registrationSearchText, "🎲 جستجوی شانسی 🎲") || strings.Contains(registrationSearchText, "💰") {
		t.Fatalf("unexpected post-registration text: %s", registrationSearchText)
	}
}

func TestProfileCompletionAdvancesInOrder(t *testing.T) {
	user := storage.User{}
	if got := nextProfileCompletionField(user); got != "name" {
		t.Fatalf("first field = %q, want name", got)
	}
	user.Name = "علی"
	if got := nextProfileCompletionField(user); got != "city" {
		t.Fatalf("second field = %q, want city", got)
	}
	user.City = "تهران"
	if got := nextProfileCompletionField(user); got != "image" {
		t.Fatalf("third field = %q, want image", got)
	}
	user.Image = "file-id"
	if got := nextProfileCompletionField(user); got != "gps" {
		t.Fatalf("fourth field = %q, want gps", got)
	}
	user.Latitude = 35.7
	if got := nextProfileCompletionField(user); got != "" {
		t.Fatalf("completed profile still requests %q", got)
	}
}

func keyboardHasCallback(keyboard [][]button, callback string) bool {
	for _, row := range keyboard {
		for _, btn := range row {
			if btn["callback_data"] == callback {
				return true
			}
		}
	}
	return false
}
