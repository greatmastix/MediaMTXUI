-- +goose Up
-- Forwarding: a stream re-streamed to another platform. The destination carries the platform's stream
-- key, so it is kept sealed (AES-GCM, like the stream's own keys); label is where it goes without the key, for pages
-- and the audit log. While enabled, the sidecar writes the destination into the path's forward list in mediamtx.yml.
CREATE TABLE stream_forwards (
  id         INTEGER PRIMARY KEY,
  stream_id  INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
  provider   TEXT NOT NULL,
  label      TEXT NOT NULL,
  dest_enc   TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
  created_at INTEGER NOT NULL,
  created_by TEXT NOT NULL
) STRICT;
CREATE INDEX stream_forwards_stream ON stream_forwards (stream_id);

-- +goose Down
DROP TABLE stream_forwards;
