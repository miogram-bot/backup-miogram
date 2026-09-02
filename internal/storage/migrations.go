package storage

const schemaSQL = `
CREATE TABLE IF NOT EXISTS admin (
	id BIGSERIAL PRIMARY KEY,
	channel_1_name TEXT,
	channel_1 TEXT,
	channel_2_name TEXT,
	channel_2 TEXT,
	channel_3_name TEXT,
	channel_3 TEXT,
	ch_cache_id TEXT,
	support TEXT,
	coin_per_invite INTEGER NOT NULL DEFAULT 7,
	coin_per_invite_profile INTEGER NOT NULL DEFAULT 8,
	coin_per_invite_invite INTEGER NOT NULL DEFAULT 5,
	coin_comprof INTEGER NOT NULL DEFAULT 5,
	card_number TEXT,
	card_holder TEXT
);
ALTER TABLE admin ADD COLUMN IF NOT EXISTS card_number TEXT;
ALTER TABLE admin ADD COLUMN IF NOT EXISTS card_holder TEXT;
ALTER TABLE admin ADD COLUMN IF NOT EXISTS admin_group_id TEXT;
INSERT INTO admin (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS amounts (
	id BIGSERIAL PRIMARY KEY,
	amount INTEGER NOT NULL DEFAULT 0,
	coin INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_amounts_amount ON amounts(amount);
CREATE UNIQUE INDEX IF NOT EXISTS idx_amounts_coin_unique ON amounts(coin);

CREATE TABLE IF NOT EXISTS users (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT UNIQUE,
	balance INTEGER NOT NULL DEFAULT 0,
	name TEXT,
	gender TEXT,
	age SMALLINT NOT NULL DEFAULT 0,
	state TEXT,
	city TEXT,
	latitude DOUBLE PRECISION NOT NULL DEFAULT 0,
	longitude DOUBLE PRECISION NOT NULL DEFAULT 0,
	image TEXT,
	num_chats INTEGER NOT NULL DEFAULT 0,
	referral TEXT,
	silent BIGINT NOT NULL DEFAULT 0,
	last_activity BIGINT NOT NULL DEFAULT 0,
	last_check_join_at BIGINT NOT NULL DEFAULT 0,
	is_coin_comprof BOOLEAN NOT NULL DEFAULT false,
	is_likes BOOLEAN NOT NULL DEFAULT true,
	same_age BOOLEAN NOT NULL DEFAULT false,
	status TEXT NOT NULL DEFAULT 'user',
	step TEXT NOT NULL DEFAULT 'start',
	prev_step TEXT NOT NULL DEFAULT 'start',
	created_at BIGINT NOT NULL DEFAULT 0,
	uniq_id TEXT UNIQUE
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS username TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_fake BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS fake_likes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS fake_source_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS ads_referral BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS ads_registration_started BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS assigned_bot TEXT;
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_gender_age_activity ON users(gender, age, last_activity DESC);
CREATE INDEX IF NOT EXISTS idx_users_gender_activity ON users(gender, last_activity DESC);
CREATE INDEX IF NOT EXISTS idx_users_state_activity ON users(state, last_activity DESC);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);
CREATE INDEX IF NOT EXISTS idx_users_location ON users(latitude, longitude);
CREATE INDEX IF NOT EXISTS idx_users_location_activity ON users(latitude, longitude, last_activity DESC);
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_users_num_chats ON users(num_chats);
CREATE INDEX IF NOT EXISTS idx_users_referral ON users(referral);

CREATE TABLE IF NOT EXISTS profile_reviews (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT NOT NULL,
	file_id TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at BIGINT NOT NULL DEFAULT 0,
	reviewed_by TEXT,
	reviewed_at BIGINT NOT NULL DEFAULT 0
);
ALTER TABLE profile_reviews ADD COLUMN IF NOT EXISTS message_id_admin INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_profile_reviews_user_status ON profile_reviews(user_id,status,id DESC);

CREATE TABLE IF NOT EXISTS support_tickets (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'open',
	created_at BIGINT NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL DEFAULT 0,
	answered_by TEXT,
	tracking_number BIGINT NOT NULL DEFAULT 0
);
ALTER TABLE support_tickets ADD COLUMN IF NOT EXISTS tracking_number BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_support_tickets_user ON support_tickets(user_id,id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_support_tracking_unique ON support_tickets(tracking_number) WHERE tracking_number<>0;

CREATE TABLE IF NOT EXISTS blocked (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT,
	target_id TEXT,
	created_at BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_blocked_pair ON blocked(user_id,target_id);
CREATE INDEX IF NOT EXISTS idx_blocked_target ON blocked(target_id,created_at);

CREATE TABLE IF NOT EXISTS friends (
	id BIGSERIAL PRIMARY KEY,
	name TEXT,
	user_id TEXT,
	target_id TEXT,
	created_at BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_friends_pair ON friends(user_id,target_id);

CREATE TABLE IF NOT EXISTS likes (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT,
	target_id TEXT,
	created_at BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_likes_pair ON likes(user_id,target_id);
CREATE INDEX IF NOT EXISTS idx_likes_target ON likes(target_id,id DESC);

CREATE TABLE IF NOT EXISTS chats (
	id BIGSERIAL PRIMARY KEY,
	user_id_1 TEXT,
	user_id_2 TEXT,
	status TEXT NOT NULL DEFAULT 'chatting',
	created_at BIGINT NOT NULL DEFAULT 0,
	started_at BIGINT NOT NULL DEFAULT 0
);
DO $$ BEGIN
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS started_at BIGINT NOT NULL DEFAULT 0;
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS ended_at BIGINT NOT NULL DEFAULT 0;
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS spent_coin_1 INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS spent_coin_2 INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS refunded_1 INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS refunded_2 INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS is_fake BOOLEAN NOT NULL DEFAULT false;
	ALTER TABLE chats ADD COLUMN IF NOT EXISTS fake_end_at BIGINT NOT NULL DEFAULT 0;
END $$;
CREATE INDEX IF NOT EXISTS idx_chats_user1_status ON chats(user_id_1,status);
CREATE INDEX IF NOT EXISTS idx_chats_user2_status ON chats(user_id_2,status);
CREATE INDEX IF NOT EXISTS idx_chats_status ON chats(status);
CREATE INDEX IF NOT EXISTS idx_chats_started_at ON chats(started_at);
CREATE INDEX IF NOT EXISTS idx_chats_fake_due ON chats(fake_end_at) WHERE status='chatting' AND is_fake=true;

CREATE TABLE IF NOT EXISTS chat_privacy (
	chat_id BIGINT NOT NULL,
	user_id TEXT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT false,
	PRIMARY KEY (chat_id,user_id)
);

CREATE TABLE IF NOT EXISTS tictactoe_games (
	id BIGSERIAL PRIMARY KEY,
	chat_id BIGINT NOT NULL,
	player_x TEXT NOT NULL,
	player_o TEXT NOT NULL,
	board TEXT NOT NULL DEFAULT '.........',
	turn TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	message_x INTEGER NOT NULL DEFAULT 0,
	message_o INTEGER NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL DEFAULT 0
);
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS chat_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS player_x TEXT NOT NULL DEFAULT '';
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS player_o TEXT NOT NULL DEFAULT '';
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS board TEXT NOT NULL DEFAULT '.........';
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS turn TEXT NOT NULL DEFAULT '';
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS message_x INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS message_o INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS created_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tictactoe_games ADD COLUMN IF NOT EXISTS updated_at BIGINT NOT NULL DEFAULT 0;
-- Earlier builds used this name for a unique active-pair index, which blocked
-- users from opening more than one board in the same conversation. Remove it
-- once and replace it with an explicitly non-unique lookup index.
DROP INDEX IF EXISTS idx_tictactoe_active_pair;
CREATE INDEX IF NOT EXISTS idx_tictactoe_pair_status ON tictactoe_games(player_x,player_o,status);
CREATE INDEX IF NOT EXISTS idx_tictactoe_player_status ON tictactoe_games(player_x,status) WHERE status='active';
CREATE INDEX IF NOT EXISTS idx_tictactoe_chat_status ON tictactoe_games(chat_id,status);

CREATE TABLE IF NOT EXISTS chatmsgs (
	id BIGSERIAL PRIMARY KEY,
	user_id_1 TEXT,
	message_id_1 INTEGER NOT NULL DEFAULT 0,
	user_id_2 TEXT,
	message_id_2 INTEGER NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_chatmsgs_user1_msg ON chatmsgs(user_id_1,message_id_1);
CREATE INDEX IF NOT EXISTS idx_chatmsgs_user2_msg ON chatmsgs(user_id_2,message_id_2);
CREATE INDEX IF NOT EXISTS idx_chatmsgs_created_at ON chatmsgs(created_at);
ALTER TABLE chatmsgs ADD COLUMN IF NOT EXISTS chat_id BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_chatmsgs_chat ON chatmsgs(chat_id,id);

CREATE TABLE IF NOT EXISTS notif (
	id BIGSERIAL PRIMARY KEY,
	type TEXT,
	user_id TEXT,
	balance INTEGER NOT NULL DEFAULT 0,
	gender TEXT,
	request_gender TEXT,
	age SMALLINT NOT NULL DEFAULT 0,
	latitude DOUBLE PRECISION NOT NULL DEFAULT 0,
	longitude DOUBLE PRECISION NOT NULL DEFAULT 0,
	user_id_2 TEXT,
	reason TEXT,
	content TEXT NOT NULL DEFAULT '0',
	status TEXT NOT NULL DEFAULT 'doing',
	date BIGINT NOT NULL DEFAULT 0
);
ALTER TABLE notif ADD COLUMN IF NOT EXISTS state TEXT;
CREATE INDEX IF NOT EXISTS idx_notif_search ON notif(type,content,status,date);
CREATE INDEX IF NOT EXISTS idx_notif_search_doing_id ON notif(content,id) WHERE type='search' AND status='doing';
CREATE INDEX IF NOT EXISTS idx_notif_search_match ON notif(content,request_gender,gender,age,id) WHERE type='search' AND status='doing';
CREATE INDEX IF NOT EXISTS idx_notif_search_connecting ON notif(date) WHERE type='search' AND status='connecting';
CREATE INDEX IF NOT EXISTS idx_notif_user_reason ON notif(user_id,user_id_2,reason,status);
CREATE INDEX IF NOT EXISTS idx_notif_user2_reason ON notif(user_id_2,reason,status);
CREATE INDEX IF NOT EXISTS idx_notif_location ON notif(latitude,longitude);

CREATE TABLE IF NOT EXISTS payments (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT,
	coins INTEGER NOT NULL DEFAULT 0,
	amount INTEGER NOT NULL DEFAULT 0,
	authority TEXT UNIQUE,
	ref_id TEXT UNIQUE,
	status TEXT NOT NULL DEFAULT 'first_level',
	created_at BIGINT NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL DEFAULT 0,
	uniq_id TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_payments_user_status ON payments(user_id,status,updated_at);
CREATE INDEX IF NOT EXISTS idx_payments_status_updated ON payments(status,updated_at);
ALTER TABLE payments ADD COLUMN IF NOT EXISTS receipt_file_id TEXT;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS message_id_admin INTEGER NOT NULL DEFAULT 0;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS reviewed_by TEXT;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS reviewed_at BIGINT NOT NULL DEFAULT 0;
	ALTER TABLE payments ADD COLUMN IF NOT EXISTS tracking_number INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE payments ADD COLUMN IF NOT EXISTS credited BOOLEAN NOT NULL DEFAULT false;
	CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_tracking_unique ON payments(tracking_number) WHERE tracking_number<>0;

CREATE TABLE IF NOT EXISTS search (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT,
	search JSONB,
	created_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_search_user_created ON search(user_id,created_at DESC);

CREATE TABLE IF NOT EXISTS states (
	id INTEGER PRIMARY KEY,
	parent INTEGER NOT NULL DEFAULT 0,
	state TEXT
);
CREATE INDEX IF NOT EXISTS idx_states_parent ON states(parent,id);
CREATE INDEX IF NOT EXISTS idx_states_parent_state ON states(parent,state);

CREATE TABLE IF NOT EXISTS telegram_assets (
	name TEXT PRIMARY KEY,
	file_id TEXT NOT NULL,
	updated_at BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS fake_users (
	id BIGSERIAL PRIMARY KEY,
	name TEXT NOT NULL,
	profile_file_id TEXT NOT NULL,
	gender TEXT,
	enabled BOOLEAN NOT NULL DEFAULT true
);
CREATE INDEX IF NOT EXISTS idx_fake_users_enabled_gender ON fake_users(enabled,gender,id);

CREATE TABLE IF NOT EXISTS cron (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT,
	chat_id TEXT,
	type TEXT,
	file_type TEXT,
	file_id TEXT,
	text TEXT,
	message_id INTEGER NOT NULL DEFAULT 0,
	send_id BIGINT NOT NULL DEFAULT 0,
	max_send_id BIGINT NOT NULL DEFAULT 0,
	message_id_edit INTEGER NOT NULL DEFAULT 0,
	count_members INTEGER NOT NULL DEFAULT 0,
	send_correct INTEGER NOT NULL DEFAULT 0,
	send_failed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cron_id ON cron(id);
`
