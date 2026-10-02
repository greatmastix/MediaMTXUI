-- +goose Up
-- Public streams: anyone may watch without a key, through the watch link or keyless addresses;
-- publishing still needs the stream key. Public is the default, for existing streams too (decided 2026-09-29).
ALTER TABLE streams ADD COLUMN public INTEGER NOT NULL DEFAULT 1 CHECK (public IN (0, 1));

-- +goose Down
ALTER TABLE streams DROP COLUMN public;
