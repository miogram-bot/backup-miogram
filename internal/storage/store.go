package storage

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

//go:embed states_seed.sql
var embeddedStatesSeed string

type Store struct {
	db    *pgxpool.Pool
	redis *redis.Client
}

func New(ctx context.Context, databaseURL string, maxConns int32, redisClient *redis.Client) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = minInt32(4, maxConns)
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 10 * time.Minute
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, redis: redisClient}, nil
}

func (s *Store) Close() {
	s.db.Close()
}

func (s *Store) DB() *pgxpool.Pool {
	return s.db
}

func (s *Store) Migrate(ctx context.Context, legacyRunPath string) error {
	if _, err := s.db.Exec(ctx, schemaSQL); err != nil {
		return err
	}
	return s.SeedStates(ctx, legacyRunPath)
}

func (s *Store) Admin(ctx context.Context) (Admin, error) {
	if s.redis != nil {
		if cached, err := s.redis.HGetAll(ctx, "miogram:admin").Result(); err == nil && len(cached) > 0 {
			return adminFromHash(cached), nil
		}
	}
	admin, err := s.adminDB(ctx)
	if err != nil {
		return admin, err
	}
	if s.redis != nil {
		_ = s.redis.HSet(ctx, "miogram:admin", adminHash(admin)).Err()
		_ = s.redis.Expire(ctx, "miogram:admin", 30*time.Second).Err()
	}
	return admin, nil
}

func (s *Store) InvalidateAdmin(ctx context.Context) {
	if s.redis != nil {
		_ = s.redis.Del(ctx, "miogram:admin").Err()
	}
}

func (s *Store) adminDB(ctx context.Context) (Admin, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id,
			coalesce(channel_1_name,''), coalesce(channel_1,''),
			coalesce(channel_2_name,''), coalesce(channel_2,''),
			coalesce(channel_3_name,''), coalesce(channel_3,''),
			coalesce(ch_cache_id,''), coalesce(support,''),
			coalesce(admin_group_id,''),
			coin_per_invite, coin_per_invite_profile, coin_per_invite_invite, coin_comprof,
			coalesce(card_number,''), coalesce(card_holder,'')
		FROM admin WHERE id=1`)
	var a Admin
	err := row.Scan(&a.ID, &a.Channel1Name, &a.Channel1, &a.Channel2Name, &a.Channel2, &a.Channel3Name, &a.Channel3, &a.ChCacheID, &a.Support, &a.AdminGroupID, &a.CoinPerInvite, &a.CoinPerInviteProfile, &a.CoinPerInviteInvite, &a.CoinCompleteProfile, &a.CardNumber, &a.CardHolder)
	return a, err
}

func (s *Store) UserByID(ctx context.Context, userID string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, userSelectSQL+" WHERE user_id=$1", userID))
}

// AssignedBot returns the durable user->bot mapping. It reports
// pgx.ErrNoRows when the user row itself does not exist; an existing row with
// empty assigned_bot yields "" (never assigned).
func (s *Store) AssignedBot(ctx context.Context, userID string) (string, error) {
	var botID *string
	err := s.db.QueryRow(ctx, `SELECT assigned_bot FROM users WHERE user_id=$1`, userID).Scan(&botID)
	if err != nil {
		return "", err
	}
	if botID == nil {
		return "", nil
	}
	return *botID, nil
}

// UpdateAssignedBot persists the durable half of the routing pair.
func (s *Store) UpdateAssignedBot(ctx context.Context, userID, botID string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET assigned_bot=$2 WHERE user_id=$1`, userID, botID)
	return err
}

func (s *Store) UserByUniqOrID(ctx context.Context, value string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, userSelectSQL+" WHERE user_id=$1 OR uniq_id=$1", value))
}

func (s *Store) UserByUniq(ctx context.Context, uniqID string, viewer *User) (User, error) {
	if viewer != nil && viewer.Latitude != 0 {
		return scanUser(s.db.QueryRow(ctx, userSelectDistanceSQL+" WHERE uniq_id=$3", viewer.Latitude, viewer.Longitude, uniqID))
	}
	return s.UserByUniqOrID(ctx, uniqID)
}

func (s *Store) QueryUsers(ctx context.Context, query string, args ...any) ([]User, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.UserID, &u.Balance, &u.Name, &u.Gender, &u.Age, &u.State, &u.City,
			&u.Latitude, &u.Longitude, &u.Image, &u.NumChats, &u.Referral, &u.Silent,
			&u.LastActivity, &u.LastCheckJoinAt, &u.IsCoinComplete, &u.IsLikes, &u.SameAge,
			&u.Status, &u.Step, &u.PrevStep, &u.CreatedAt, &u.UniqID, &u.Distance, &u.Username,
			&u.IsFake, &u.FakeLikes, &u.FakeSourceID, &u.ADSReferral, &u.ADSRegistrationStarted,
		); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, userID, referral string, now int64) (User, error) {
	uniq, err := s.RandomUniq(ctx, "users", 10)
	if err != nil {
		return User{}, err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO users (user_id, referral, last_activity, created_at, uniq_id)
		VALUES ($1, nullif($2,''), $3, $3, $4)
		ON CONFLICT (user_id) DO NOTHING`, userID, referral, now, uniq)
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, userID)
}

func (s *Store) UpdateUserActivity(ctx context.Context, userID string, now int64) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET last_activity=$2 WHERE user_id=$1`, userID, now)
	return err
}

func (s *Store) UpdateUserActivityWithUsername(ctx context.Context, userID, username string, now int64) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET last_activity=$2,username=COALESCE(NULLIF($3,''),username) WHERE user_id=$1`, userID, now, username)
	return err
}

func (s *Store) UpdateUserStep(ctx context.Context, userID, step string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET step=$2 WHERE user_id=$1`, userID, step)
	return err
}

func (s *Store) UpdateUserStepPrev(ctx context.Context, userID, step, prev string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET step=$2, prev_step=$3 WHERE user_id=$1`, userID, step, prev)
	return err
}

func (s *Store) AddBalance(ctx context.Context, userID string, delta int) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET balance=balance+$2 WHERE user_id=$1`, userID, delta)
	return err
}

func (s *Store) SetBalance(ctx context.Context, userID string, balance int) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET balance=$2 WHERE user_id=$1`, userID, balance)
	return err
}

func (s *Store) ActiveChat(ctx context.Context, userID string) (Chat, error) {
	row := s.db.QueryRow(ctx, `SELECT id,user_id_1,user_id_2,status,created_at,started_at,ended_at,spent_coin_1,spent_coin_2,refunded_1,refunded_2,is_fake,fake_end_at FROM chats WHERE (user_id_1=$1 OR user_id_2=$1) AND status='chatting' LIMIT 1`, userID)
	var c Chat
	err := row.Scan(&c.ID, &c.UserID1, &c.UserID2, &c.Status, &c.CreatedAt, &c.StartedAt, &c.EndedAt, &c.SpentCoin1, &c.SpentCoin2, &c.Refunded1, &c.Refunded2, &c.IsFake, &c.FakeEndAt)
	return c, err
}

func (s *Store) IsChatting(ctx context.Context, userID string) bool {
	var id int64
	return s.db.QueryRow(ctx, `SELECT id FROM chats WHERE (user_id_1=$1 OR user_id_2=$1) AND status='chatting' LIMIT 1`, userID).Scan(&id) == nil
}

func (s *Store) CreateChat(ctx context.Context, userID1, userID2 string, now int64, cost1, cost2 int) error {
	if userID1 == userID2 {
		return errors.New("cannot create chat with the same user")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT user_id FROM users WHERE user_id=$1 OR user_id=$2 FOR UPDATE`, userID1, userID2); err != nil {
		return err
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM chats WHERE status='chatting' AND (user_id_1=$1 OR user_id_2=$1 OR user_id_1=$2 OR user_id_2=$2)`, userID1, userID2).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return errors.New("one of users is already chatting")
	}
	if _, err = tx.Exec(ctx, `UPDATE notif SET status='end' WHERE type='search' AND (user_id=$1 OR user_id=$2)`, userID1, userID2); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chats (user_id_1,user_id_2,status,created_at,started_at,spent_coin_1,spent_coin_2) VALUES ($1,$2,'chatting',$3,$3,$4,$5)`, userID1, userID2, now, cost1, cost2); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET balance=balance-$2, num_chats=num_chats+1, step=$3 WHERE user_id=$1`, userID1, cost1, "chatting;"+userID2); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET balance=balance-$2, num_chats=num_chats+1, step=$3 WHERE user_id=$1`, userID2, cost2, "chatting;"+userID1); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateFakeChatFromSearch atomically claims an expired search and creates a
// short-lived synthetic user snapshot from an imported fake_users record.
func (s *Store) CreateFakeChatFromSearch(ctx context.Context, notificationID, now int64) (Chat, User, User, error) {
	var chat Chat
	var realUser, fakeUser User
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return chat, realUser, fakeUser, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var n Notification
	err = tx.QueryRow(ctx, `SELECT id,user_id,balance,coalesce(gender,''),coalesce(request_gender,''),age,latitude,longitude,coalesce(state,''),content,status,date FROM notif WHERE id=$1 FOR UPDATE`, notificationID).
		Scan(&n.ID, &n.UserID, &n.Balance, &n.Gender, &n.RequestGender, &n.Age, &n.Latitude, &n.Longitude, &n.State, &n.Content, &n.Status, &n.Date)
	if err != nil || n.Status != "doing" || n.Date+60 > now {
		if err == nil {
			err = errors.New("search is not ready for fake fallback")
		}
		return chat, realUser, fakeUser, err
	}
	realUser, err = scanUser(tx.QueryRow(ctx, userSelectSQL+" WHERE user_id=$1 FOR UPDATE", n.UserID))
	if err != nil {
		return chat, realUser, fakeUser, err
	}
	if IsUserChattingTx(ctx, tx, realUser.UserID) {
		return chat, realUser, fakeUser, errors.New("user is already chatting")
	}
	var fakeID int64
	var fakeName, profileFileID, importedGender string
	var maxFakeID int64
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM fake_users WHERE enabled=true`).Scan(&maxFakeID); err != nil || maxFakeID == 0 {
		if err == nil {
			err = pgx.ErrNoRows
		}
		return chat, realUser, fakeUser, err
	}
	startFakeID := int64(1) + secureRandomInt64(maxFakeID)
	err = tx.QueryRow(ctx, `SELECT id,name,profile_file_id,coalesce(gender,'') FROM fake_users WHERE enabled=true AND id>=$2 AND ($1='' OR coalesce(gender,'')='' OR gender=$1) ORDER BY id LIMIT 1`, n.RequestGender, startFakeID).
		Scan(&fakeID, &fakeName, &profileFileID, &importedGender)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id,name,profile_file_id,coalesce(gender,'') FROM fake_users WHERE enabled=true AND ($1='' OR coalesce(gender,'')='' OR gender=$1) ORDER BY id LIMIT 1`, n.RequestGender).
			Scan(&fakeID, &fakeName, &profileFileID, &importedGender)
	}
	if err != nil {
		return chat, realUser, fakeUser, err
	}
	gender := importedGender
	if n.RequestGender != "" {
		gender = n.RequestGender
	}
	if gender != "boy" && gender != "girl" {
		if secureRandomInt(2) == 0 {
			gender = "boy"
		} else {
			gender = "girl"
		}
	}
	age := 14 + secureRandomInt(21)
	if n.Age > 0 {
		age = n.Age
		if age < 14 {
			age = 14
		}
		if age > 34 {
			age = 34
		}
	}
	state := n.State
	if n.Content == "normal" || state == "" {
		_ = tx.QueryRow(ctx, `SELECT state FROM states WHERE parent=0 ORDER BY random() LIMIT 1`).Scan(&state)
	}
	if n.Content == "gps" && realUser.State != "" {
		state = realUser.State
	}
	city := ""
	if n.Content == "gps" && realUser.City != "" {
		city = realUser.City
	} else {
		_ = tx.QueryRow(ctx, `SELECT child.state FROM states child JOIN states province ON province.id=child.parent WHERE province.parent=0 AND province.state=$1 ORDER BY random() LIMIT 1`, state).Scan(&city)
	}
	uniqID := randomAlphaNum(10)
	fakeUserID := fmt.Sprintf("fake:%d:%d", fakeID, notificationID)
	likes := 5 + secureRandomInt(496)
	latitude, longitude := float64(0), float64(0)
	if n.Content == "gps" && n.Latitude != 0 {
		// Keep the generated profile genuinely nearby without copying the exact
		// requester location into the synthetic record.
		latitude = n.Latitude + float64(secureRandomInt(2001)-1000)/100000
		longitude = n.Longitude + float64(secureRandomInt(2001)-1000)/100000
	}
	_, err = tx.Exec(ctx, `INSERT INTO users (user_id,name,gender,age,state,city,latitude,longitude,image,last_activity,created_at,uniq_id,is_fake,fake_likes,fake_source_id,step) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$11,true,$12,$13,$14)`, fakeUserID, fakeName, gender, age, state, city, latitude, longitude, profileFileID, now, uniqID, likes, fakeID, "chatting;"+realUser.UserID)
	if err != nil {
		return chat, realUser, fakeUser, err
	}
	fakeEndAt := now + int64(10+secureRandomInt(6))
	err = tx.QueryRow(ctx, `INSERT INTO chats (user_id_1,user_id_2,status,created_at,started_at,spent_coin_1,spent_coin_2,is_fake,fake_end_at) VALUES ($1,$2,'chatting',$3,$3,$4,0,true,$5) RETURNING id`, realUser.UserID, fakeUserID, now, n.Balance, fakeEndAt).Scan(&chat.ID)
	if err != nil {
		return chat, realUser, fakeUser, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET balance=balance-$2,num_chats=num_chats+1,step=$3 WHERE user_id=$1`, realUser.UserID, n.Balance, "chatting;"+fakeUserID); err != nil {
		return chat, realUser, fakeUser, err
	}
	if _, err = tx.Exec(ctx, `UPDATE notif SET status='end' WHERE id=$1`, notificationID); err != nil {
		return chat, realUser, fakeUser, err
	}
	chat.UserID1, chat.UserID2, chat.Status = realUser.UserID, fakeUserID, "chatting"
	chat.CreatedAt, chat.StartedAt, chat.SpentCoin1, chat.IsFake, chat.FakeEndAt = now, now, n.Balance, true, fakeEndAt
	fakeUser = User{UserID: fakeUserID, Name: fakeName, Gender: gender, Age: age, State: state, City: city, Latitude: latitude, Longitude: longitude, Image: profileFileID, LastActivity: now, CreatedAt: now, UniqID: uniqID, IsFake: true, FakeLikes: likes, FakeSourceID: fakeID, IsLikes: true, Step: "chatting;" + realUser.UserID}
	return chat, realUser, fakeUser, tx.Commit(ctx)
}

func IsUserChattingTx(ctx context.Context, tx pgx.Tx, userID string) bool {
	var exists bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chats WHERE status='chatting' AND (user_id_1=$1 OR user_id_2=$1))`, userID).Scan(&exists)
	return exists
}

func secureRandomInt(max int) int {
	return int(secureRandomInt64(int64(max)))
}

func secureRandomInt64(max int64) int64 {
	if max <= 1 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		return time.Now().UnixNano() % max
	}
	return n.Int64()
}

func (s *Store) EndChat(ctx context.Context, chatID, now int64) (EndChatResult, error) {
	result := EndChatResult{ChatID: chatID, EndedAt: now}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var spent1, spent2 int
	if err = tx.QueryRow(ctx, `SELECT user_id_1,user_id_2,status,spent_coin_1,spent_coin_2 FROM chats WHERE id=$1 FOR UPDATE`, chatID).Scan(&result.UserID1, &result.UserID2, &status, &spent1, &spent2); err != nil {
		return result, err
	}
	if status != "chatting" {
		return result, errors.New("chat is already ended")
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM chatmsgs WHERE chat_id=$1`, chatID).Scan(&result.Messages); err != nil {
		return result, err
	}
	if result.Messages < 5 {
		result.Refund1 = spent1
		result.Refund2 = spent2
	}
	if _, err = tx.Exec(ctx, `UPDATE chats SET status='end',ended_at=$2,refunded_1=$3,refunded_2=$4 WHERE id=$1`, chatID, now, result.Refund1, result.Refund2); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tictactoe_games SET status='ended',updated_at=$2 WHERE chat_id=$1 AND status='active'`, chatID, now); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET step='start', balance = balance + CASE WHEN user_id=$1 THEN $3::int ELSE $4::int END WHERE user_id IN ($1,$2) AND step LIKE 'chatting;%'`, result.UserID1, result.UserID2, result.Refund1, result.Refund2); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) RandomUniq(ctx context.Context, table string, size int) (string, error) {
	for i := 0; i < 20; i++ {
		value := randomAlphaNum(size)
		var exists bool
		var err error
		switch table {
		case "users":
			err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE uniq_id=$1)`, value).Scan(&exists)
		case "payments":
			err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payments WHERE uniq_id=$1)`, value).Scan(&exists)
		default:
			return "", errors.New("unknown uniq table")
		}
		if err != nil {
			return "", err
		}
		if !exists {
			return value, nil
		}
	}
	return "", errors.New("could not allocate uniq id")
}

func (s *Store) StateNames(ctx context.Context, parent int) ([]State, error) {
	cacheKey := fmt.Sprintf("miogram:states:%d", parent)
	if s.redis != nil {
		if values, err := s.redis.LRange(ctx, cacheKey, 0, -1).Result(); err == nil && len(values) > 0 {
			states := make([]State, 0, len(values))
			for _, value := range values {
				parts := strings.SplitN(value, ":", 2)
				if len(parts) != 2 {
					continue
				}
				id, _ := strconv.Atoi(parts[0])
				states = append(states, State{ID: id, Parent: parent, Name: parts[1]})
			}
			return states, nil
		}
	}
	rows, err := s.db.Query(ctx, `SELECT id,parent,state FROM states WHERE parent=$1 ORDER BY id ASC`, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var st State
		if err := rows.Scan(&st.ID, &st.Parent, &st.Name); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	if s.redis != nil && len(out) > 0 {
		values := make([]any, 0, len(out))
		for _, st := range out {
			values = append(values, fmt.Sprintf("%d:%s", st.ID, st.Name))
		}
		_ = s.redis.Del(ctx, cacheKey).Err()
		_ = s.redis.RPush(ctx, cacheKey, values...).Err()
		_ = s.redis.Expire(ctx, cacheKey, 24*time.Hour).Err()
	}
	return out, rows.Err()
}

func (s *Store) StateExists(ctx context.Context, name string, parent int) bool {
	var id int
	return s.db.QueryRow(ctx, `SELECT id FROM states WHERE parent=$1 AND state=$2 LIMIT 1`, parent, name).Scan(&id) == nil
}

func (s *Store) ParentStateID(ctx context.Context, name string) (int, error) {
	var id int
	err := s.db.QueryRow(ctx, `SELECT id FROM states WHERE parent=0 AND state=$1`, name).Scan(&id)
	return id, err
}

func (s *Store) SeedStates(ctx context.Context, legacyRunPath string) error {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM states`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	states, err := parseStatesFromFile(legacyRunPath)
	if err != nil || len(states) == 0 {
		states = parseStatesData(embeddedStatesSeed)
	}
	if len(states) == 0 {
		states = fallbackProvinces()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, st := range states {
		if _, err := tx.Exec(ctx, `INSERT INTO states (id,parent,state) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING`, st.ID, st.Parent, st.Name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.UserID, &u.Balance, &u.Name, &u.Gender, &u.Age, &u.State, &u.City,
		&u.Latitude, &u.Longitude, &u.Image, &u.NumChats, &u.Referral, &u.Silent,
		&u.LastActivity, &u.LastCheckJoinAt, &u.IsCoinComplete, &u.IsLikes, &u.SameAge,
		&u.Status, &u.Step, &u.PrevStep, &u.CreatedAt, &u.UniqID, &u.Distance, &u.Username,
		&u.IsFake, &u.FakeLikes, &u.FakeSourceID, &u.ADSReferral, &u.ADSRegistrationStarted,
	)
	return u, err
}

const UserColumns = `
	id, user_id, balance,
	coalesce(name,''), coalesce(gender,''), age, coalesce(state,''), coalesce(city,''),
	latitude, longitude, coalesce(image,''), num_chats, coalesce(referral,''), silent,
	last_activity, last_check_join_at, is_coin_comprof::bool, is_likes::bool, same_age::bool,
	status, step, prev_step, created_at, coalesce(uniq_id,''), 0::float8 AS distance, coalesce(username,''),
	is_fake, fake_likes, fake_source_id, ads_referral, ads_registration_started`

const userSelectSQL = `SELECT ` + UserColumns + ` FROM users`

const userSelectDistanceSQL = `
	SELECT id, user_id, balance,
		coalesce(name,''), coalesce(gender,''), age, coalesce(state,''), coalesce(city,''),
		latitude, longitude, coalesce(image,''), num_chats, coalesce(referral,''), silent,
		last_activity, last_check_join_at, is_coin_comprof::bool, is_likes::bool, same_age::bool,
		status, step, prev_step, created_at, coalesce(uniq_id,''),
		CASE WHEN latitude=0 OR longitude=0 THEN 0 ELSE
		(6371 * acos(least(1, greatest(-1,
			cos(radians($1)) * cos(radians(latitude)) * cos(radians(longitude) - radians($2)) +
			sin(radians($1)) * sin(radians(latitude))
		)))) END AS distance,
		coalesce(username,''), is_fake, fake_likes, fake_source_id, ads_referral, ads_registration_started
	FROM users`

func parseStatesFromFile(path string) ([]State, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("state seed file path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseStatesData(string(raw)), nil
}

func parseStatesData(raw string) []State {
	re := regexp.MustCompile(`\((\d+),\s*(\d+),\s*'([^']*)'\)`)
	matches := re.FindAllStringSubmatch(raw, -1)
	out := make([]State, 0, len(matches))
	for _, m := range matches {
		id, _ := strconv.Atoi(m[1])
		parent, _ := strconv.Atoi(m[2])
		out = append(out, State{ID: id, Parent: parent, Name: m[3]})
	}
	return out
}

func fallbackProvinces() []State {
	names := []string{"آذربایجان شرقی", "آذربایجان غربی", "اردبیل", "اصفهان", "البرز", "ایلام", "بوشهر", "تهران", "چهارمحال و بختیاری", "خراسان جنوبی", "خراسان رضوی", "خراسان شمالی", "خوزستان", "زنجان", "سمنان", "سیستان و بلوچستان", "فارس", "قزوین", "قم", "کردستان", "کرمان", "کرمانشاه", "کهگیلویه و بویراحمد", "گلستان", "گیلان", "لرستان", "مازندران", "مرکزی", "هرمزگان", "همدان", "یزد"}
	out := make([]State, 0, len(names))
	for i, name := range names {
		out = append(out, State{ID: i + 1, Parent: 0, Name: name})
	}
	return out
}

func randomAlphaNum(size int) string {
	const chars = "1234567890qwertyuiopasdfghjklzxcvbnmQWERTYUIOPASDFGHJKLZXCVBNM"
	if size <= 0 {
		size = 10
	}
	buf := make([]byte, size)
	random := make([]byte, size)
	if _, err := rand.Read(random); err != nil {
		token := make([]byte, 6)
		_, _ = rand.Read(token)
		return hex.EncodeToString(token)
	}
	for i := range buf {
		buf[i] = chars[int(random[i])%len(chars)]
	}
	return string(buf)
}

func adminFromHash(h map[string]string) Admin {
	intVal := func(k string, fallback int) int {
		if n, err := strconv.Atoi(h[k]); err == nil {
			return n
		}
		return fallback
	}
	id, _ := strconv.ParseInt(h["id"], 10, 64)
	return Admin{
		ID: id, Channel1Name: h["channel_1_name"], Channel1: h["channel_1"],
		Channel2Name: h["channel_2_name"], Channel2: h["channel_2"],
		Channel3Name: h["channel_3_name"], Channel3: h["channel_3"],
		ChCacheID: h["ch_cache_id"], Support: h["support"],
		AdminGroupID: h["admin_group_id"],
		CardNumber:   h["card_number"], CardHolder: h["card_holder"],
		CoinPerInvite: intVal("coin_per_invite", 7), CoinPerInviteProfile: intVal("coin_per_invite_profile", 8),
		CoinPerInviteInvite: intVal("coin_per_invite_invite", 5), CoinCompleteProfile: intVal("coin_comprof", 5),
	}
}

func adminHash(a Admin) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(a.ID, 10), "channel_1_name": a.Channel1Name, "channel_1": a.Channel1,
		"channel_2_name": a.Channel2Name, "channel_2": a.Channel2, "channel_3_name": a.Channel3Name, "channel_3": a.Channel3,
		"ch_cache_id": a.ChCacheID, "support": a.Support, "admin_group_id": a.AdminGroupID, "coin_per_invite": a.CoinPerInvite,
		"coin_per_invite_profile": a.CoinPerInviteProfile, "coin_per_invite_invite": a.CoinPerInviteInvite, "coin_comprof": a.CoinCompleteProfile,
		"card_number": a.CardNumber, "card_holder": a.CardHolder,
	}
}

func minInt32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}
