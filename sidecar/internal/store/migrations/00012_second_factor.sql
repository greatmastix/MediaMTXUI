-- +goose Up
-- Second factors and step-up. A session's verified_at is when it last proved who it is (signing in, or a
-- step-up); admin-level changes ask again after an hour unless the user opted out. TOTP: the secret is sealed like
-- stream keys ('' when off) and the last time step used is kept, so a code works once. Recovery codes are stored as
-- hashes and work once each. Passkeys: the WebAuthn credential (public key, sign counter, flags) as the library's JSON;
-- webauthn_id is the user handle passkeys carry (random, never the user id).
ALTER TABLE sessions ADD COLUMN verified_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN step_up_opt_out INTEGER NOT NULL DEFAULT 0 CHECK (step_up_opt_out IN (0, 1));
ALTER TABLE users ADD COLUMN webauthn_id BLOB;

CREATE TABLE recovery_codes (
  user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  used_at   INTEGER,
  PRIMARY KEY (user_id, code_hash)
) STRICT;

CREATE TABLE passkeys (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  credential_id BLOB NOT NULL UNIQUE,
  credential    TEXT NOT NULL,
  name          TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER
) STRICT;
CREATE INDEX passkeys_user ON passkeys (user_id);
CREATE UNIQUE INDEX users_webauthn_id ON users (webauthn_id) WHERE webauthn_id IS NOT NULL;

-- +goose Down
DROP INDEX users_webauthn_id;
DROP TABLE passkeys;
DROP TABLE recovery_codes;
ALTER TABLE users DROP COLUMN webauthn_id;
ALTER TABLE users DROP COLUMN step_up_opt_out;
ALTER TABLE users DROP COLUMN totp_last_step;
ALTER TABLE users DROP COLUMN totp_enc;
ALTER TABLE sessions DROP COLUMN verified_at;
