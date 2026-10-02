-- +goose Up
-- Saved multi-view layouts, per user. data is the UI's JSON (versioned by its own "version" field); the sidecar checks
-- only its size and shape, so the UI can evolve it.
CREATE TABLE user_layouts (
  user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  data       TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, name)
) STRICT;

-- +goose Down
DROP TABLE user_layouts;
