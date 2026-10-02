-- +goose Up
-- The long-term history . One row per minute the sidecar saw MediaMTX: mean bitrates and the most
-- paths, online paths, readers and clients at once; and per path, for minutes it had traffic or readers (a minute
-- with a global row and no path row means the path was idle). Kept MTXUI_HISTORY_DAYS (30) days.
CREATE TABLE history_minutes (
  t       INTEGER PRIMARY KEY, -- the minute's start, Unix ms
  in_bps  REAL NOT NULL,
  out_bps REAL NOT NULL,
  paths   INTEGER NOT NULL,
  online  INTEGER NOT NULL,
  readers INTEGER NOT NULL,
  clients INTEGER NOT NULL
) STRICT;

CREATE TABLE path_history_minutes (
  path    TEXT NOT NULL,
  t       INTEGER NOT NULL,
  in_bps  REAL NOT NULL,
  out_bps REAL NOT NULL,
  readers INTEGER NOT NULL,
  PRIMARY KEY (path, t)
) STRICT, WITHOUT ROWID;

CREATE INDEX path_history_minutes_t ON path_history_minutes (t);

-- +goose Down
DROP TABLE path_history_minutes;
DROP TABLE history_minutes;
