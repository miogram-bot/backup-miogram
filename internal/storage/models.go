package storage

type Admin struct {
	ID                   int64
	Channel1Name         string
	Channel1             string
	Channel2Name         string
	Channel2             string
	Channel3Name         string
	Channel3             string
	ChCacheID            string
	Support              string
	AdminGroupID         string
	CoinPerInvite        int
	CoinPerInviteProfile int
	CoinPerInviteInvite  int
	CoinCompleteProfile  int
	CardNumber           string
	CardHolder           string
}

type User struct {
	ID                     int64
	UserID                 string
	Balance                int
	Name                   string
	Gender                 string
	Age                    int
	State                  string
	City                   string
	Latitude               float64
	Longitude              float64
	Image                  string
	NumChats               int
	Referral               string
	Silent                 int64
	LastActivity           int64
	LastCheckJoinAt        int64
	IsCoinComplete         bool
	IsLikes                bool
	SameAge                bool
	Status                 string
	Step                   string
	PrevStep               string
	CreatedAt              int64
	UniqID                 string
	Distance               float64
	Username               string
	IsFake                 bool
	FakeLikes              int
	FakeSourceID           int64
	ADSReferral            bool
	ADSRegistrationStarted bool
	ProfilePath            string
}

type Amount struct {
	ID     int64
	Amount int
	Coin   int
}

type Payment struct {
	ID             int64
	UserID         string
	Coins          int
	Amount         int
	Authority      string
	RefID          string
	Status         string
	CreatedAt      int64
	UpdatedAt      int64
	UniqID         string
	TrackingNumber int
}

type Notification struct {
	ID            int64
	Type          string
	UserID        string
	Balance       int
	Gender        string
	RequestGender string
	Age           int
	Latitude      float64
	Longitude     float64
	UserID2       string
	Reason        string
	Content       string
	Status        string
	Date          int64
	Distance      float64
	State         string
}

type Chat struct {
	ID         int64
	UserID1    string
	UserID2    string
	Status     string
	CreatedAt  int64
	StartedAt  int64
	EndedAt    int64
	SpentCoin1 int
	SpentCoin2 int
	Refunded1  int
	Refunded2  int
	IsFake     bool
	FakeEndAt  int64
}

type EndChatResult struct {
	ChatID   int64
	UserID1  string
	UserID2  string
	Refund1  int
	Refund2  int
	Messages int
	EndedAt  int64
}

type ChatMessage struct {
	ID         int64
	UserID1    string
	MessageID1 int
	UserID2    string
	MessageID2 int
	CreatedAt  int64
	ChatID     int64
}

type State struct {
	ID     int
	Parent int
	Name   string
}

type SearchRow struct {
	ID        int64
	UserID    string
	Search    string
	CreatedAt int64
}

type Cron struct {
	ID            int64
	UserID        string
	ChatID        string
	Type          string
	FileType      string
	FileID        string
	Text          string
	MessageID     int
	SendID        int64
	MaxSendID     int64
	MessageIDEdit int
	CountMembers  int
	SendCorrect   int
	SendFailed    int
}
