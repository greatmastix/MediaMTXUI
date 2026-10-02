# Offline clips

The holding screen's "Offline screen": a 4 s loop of MediaMTX's own offline frame with stereo 48 kHz silence, one per
video format (720p and 1080p at 50 and 60 fps) and audio (AAC for RTMP/SRT encoders, Opus for WHIP), so it matches the
stream's encoder. Made by `make.sh` (`./dev offline-clips`, with the pinned MediaMTX ffmpeg image; the output is
reproducible), embedded in the sidecar, and installed into the holding directory at startup.

`offline.png` is the first frame of `internal/stream/offline_h264.mp4` from MediaMTX
(https://github.com/bluenviron/mediamtx), MIT License, Copyright (c) 2019 aler9.
