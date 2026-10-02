-- +goose Up
-- Streams: a MediaMTX path with a title, an optional owner and its two keys. A key is an ordinary
-- stream credential; *_key_enc keeps an AES-GCM-encrypted copy of its secret (key derived from state/credential-key)
-- so the stream page can show it again.
CREATE TABLE streams (
  id               INTEGER PRIMARY KEY,
  name             TEXT NOT NULL UNIQUE,
  title            TEXT NOT NULL,
  owner_id         INTEGER REFERENCES users (id) ON DELETE SET NULL,
  target           TEXT NOT NULL DEFAULT '',
  publish_key_id   INTEGER REFERENCES stream_credentials (id) ON DELETE SET NULL,
  publish_key_enc  TEXT,
  playback_key_id  INTEGER REFERENCES stream_credentials (id) ON DELETE SET NULL,
  playback_key_enc TEXT,
  created_at       INTEGER NOT NULL,
  created_by       TEXT NOT NULL
) STRICT;
CREATE INDEX streams_owner_id ON streams (owner_id);

-- One-time join codes for invited accounts. code_hash is the SHA-256 of the code; the code itself is shown once.
CREATE TABLE join_codes (
  code_hash  TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  created_by TEXT NOT NULL,
  used_at    INTEGER
) STRICT;
CREATE INDEX join_codes_user_id ON join_codes (user_id);

-- +goose Down
DROP TABLE join_codes;
DROP TABLE streams;
