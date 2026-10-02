-- +goose Up
-- Guest keys: time-limited publish or read keys for one stream, made by whoever manages it. The key is
-- an ordinary credential scoped to the stream's path, with an expiry; this table ties it to the stream. The secret is
-- shown once and never stored in the clear. Deleting a stream drops the link; the credential stays, revoked, for the
-- audit trail.
CREATE TABLE stream_guest_keys (
  credential_id INTEGER PRIMARY KEY REFERENCES stream_credentials (id),
  stream_id     INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('publish', 'read')),
  label         TEXT NOT NULL
) STRICT;
CREATE INDEX stream_guest_keys_stream ON stream_guest_keys (stream_id);

-- +goose Down
DROP TABLE stream_guest_keys;
