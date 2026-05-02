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
