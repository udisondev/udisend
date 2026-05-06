-- udisend messenger storage schema. Single SQLite DB per messenger instance.

CREATE TABLE IF NOT EXISTS contacts (
    destination_hash TEXT PRIMARY KEY,
    ed_pub BLOB NOT NULL,
    x_pub BLOB NOT NULL,
    alias TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL,
    verified INTEGER NOT NULL DEFAULT 0,
    added_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    peer_hash TEXT NOT NULL,
    direction TEXT NOT NULL CHECK(direction IN ('in', 'out')),
    kind INTEGER NOT NULL,         -- chat.MessageKind
    body BLOB NOT NULL,
    status INTEGER NOT NULL DEFAULT 0,   -- 0=pending, 1=sent, 2=acked, 3=failed
    ts INTEGER NOT NULL                  -- unix nanos
);
CREATE INDEX IF NOT EXISTS messages_peer_ts ON messages(peer_hash, ts);

CREATE TABLE IF NOT EXISTS outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    peer_hash TEXT NOT NULL,
    payload BLOB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_attempt INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS outbox_peer ON outbox(peer_hash);

-- seen_peers caches network addresses we've successfully reached. On a
-- subsequent start with no CLI --bootstrap, the runtime seeds itself from
-- this table (design.md §7 "Bootstrap-список", item 2).
CREATE TABLE IF NOT EXISTS seen_peers (
    address TEXT PRIMARY KEY,
    last_seen INTEGER NOT NULL,
    success_count INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS seen_peers_last_seen ON seen_peers(last_seen DESC);

-- bootstrap_overrides — manually-curated bootstrap addresses managed from
-- the webui (Settings → Bootstrap). Tried before seen_peers cache and
-- community defaults, after the CLI -bootstrap override. enabled=0 keeps
-- a row but skips it during bootstrap; last_status is one of '', 'ok',
-- 'fail' (most recent attempt).
CREATE TABLE IF NOT EXISTS bootstrap_overrides (
    address TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 1,
    note TEXT NOT NULL DEFAULT '',
    added_at INTEGER NOT NULL,
    last_status TEXT NOT NULL DEFAULT '',
    last_status_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS bootstrap_overrides_enabled
    ON bootstrap_overrides(enabled, added_at DESC);

-- WebUI authentication. Single-user system, so auth_credentials is
-- single-row (CHECK enforces id=1). Used only when the HTTP server binds
-- to a non-loopback address; loopback keeps the legacy URL-token flow.
CREATE TABLE IF NOT EXISTS auth_credentials (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    passphrase_hash TEXT NOT NULL,
    totp_secret BLOB,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_recovery_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    hash TEXT NOT NULL,
    consumed_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    last_seen INTEGER NOT NULL,
    remote_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS auth_sessions_last_seen ON auth_sessions(last_seen);

CREATE TABLE IF NOT EXISTS auth_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER NOT NULL,
    event TEXT NOT NULL,
    remote_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS auth_log_ts ON auth_log(ts DESC);

-- app_settings is a generic key/value bag for runtime-tunable preferences
-- managed from the webui (log level, history retention, ICE fallback,
-- etc.). Values are TEXT so handlers parse the shape they expect.
CREATE TABLE IF NOT EXISTS app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- ice_overrides — user-curated STUN/TURN servers managed from Settings →
-- Network. Merged with peers auto-discovered through presence (via the
-- snapshot endpoint) when constructing the browser's RTCPeerConnection
-- iceServers list. enabled=0 keeps a row but skips it.
CREATE TABLE IF NOT EXISTS ice_overrides (
    url TEXT PRIMARY KEY,
    username TEXT NOT NULL DEFAULT '',
    credential TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    added_at INTEGER NOT NULL
);
