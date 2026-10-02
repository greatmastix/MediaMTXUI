-- +goose Up
-- An own holding clip comes in two versions, one per audio an encoder may send: AAC (RTMP, SRT) and Opus (WHIP), so
-- switching what the encoder sends needs no new upload. The single file becomes the version of the stream's audio.
ALTER TABLE streams ADD COLUMN clip_aac TEXT NOT NULL DEFAULT '';
ALTER TABLE streams ADD COLUMN clip_opus TEXT NOT NULL DEFAULT '';
UPDATE streams SET clip_aac = holding_file WHERE audio = 'aac';
UPDATE streams SET clip_opus = holding_file WHERE audio = 'opus';
ALTER TABLE streams DROP COLUMN holding_file;

-- +goose Down
ALTER TABLE streams ADD COLUMN holding_file TEXT NOT NULL DEFAULT '';
UPDATE streams SET holding_file = CASE audio WHEN 'opus' THEN clip_opus ELSE clip_aac END;
ALTER TABLE streams DROP COLUMN clip_opus;
ALTER TABLE streams DROP COLUMN clip_aac;
