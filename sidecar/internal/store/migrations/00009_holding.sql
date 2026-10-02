-- +goose Up
-- Holding screens: what a stream shows while nobody streams to it (MediaMTX's alwaysAvailable). holding is '' (none),
-- 'builtin' (MediaMTX's own offline screen) or 'file' (holding_file, an MP4 in the holding directory). A publisher must
-- match the holding clip's tracks, so each stream says which audio its encoder sends: 'aac' (RTMP, SRT) or 'opus'
-- (WHIP).
ALTER TABLE streams ADD COLUMN audio TEXT NOT NULL DEFAULT 'aac' CHECK (audio IN ('aac', 'opus'));
ALTER TABLE streams ADD COLUMN holding TEXT NOT NULL DEFAULT '' CHECK (holding IN ('', 'builtin', 'file'));
ALTER TABLE streams ADD COLUMN holding_file TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE streams DROP COLUMN holding_file;
ALTER TABLE streams DROP COLUMN holding;
ALTER TABLE streams DROP COLUMN audio;
