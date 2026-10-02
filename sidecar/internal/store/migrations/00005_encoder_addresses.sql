-- +goose Up
-- Addresses that published to a stream (automatic exposure): the stream's port stays open to them for
-- 30 days after last_seen, so an encoder starting cold the next day gets in. At most three per stream are kept.
CREATE TABLE encoder_addresses (
  stream_id INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
  port      TEXT NOT NULL,
  ip        TEXT NOT NULL,
  last_seen INTEGER NOT NULL,
  PRIMARY KEY (stream_id, port, ip)
) STRICT;

-- +goose Down
DROP TABLE encoder_addresses;
