-- +goose Up
-- The stream's video format (resolution and frame rate): its holding clips are in it, so the hand-over to the encoder
-- changes neither size nor rate.
ALTER TABLE streams ADD COLUMN format TEXT NOT NULL DEFAULT '1080p50'
  CHECK (format IN ('720p50', '720p60', '1080p50', '1080p60'));

-- +goose Down
ALTER TABLE streams DROP COLUMN format;
