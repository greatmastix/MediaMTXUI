#!/bin/sh
# Makes the holding screen's offline clips from offline.png (MediaMTX's own offline frame): one per format and audio,
# 4 s loops of a still frame with stereo 48 kHz silence, without B-frames (MediaMTX refuses WebRTC readers of H.264 with
# B-frames). Run by `./dev offline-clips` inside the pinned MediaMTX ffmpeg image, in this directory; the outputs are
# committed and embedded in the sidecar.
set -eu
for f in 720p50:1280:720:50 720p60:1280:720:60 1080p50:1920:1080:50 1080p60:1920:1080:60; do
  name=${f%%:*}
  rest=${f#*:}
  w=${rest%%:*}
  rest=${rest#*:}
  h=${rest%%:*}
  fps=${rest#*:}
  for a in aac opus; do
    if [ "$a" = aac ]; then codec=aac; else codec=libopus; fi
    ffmpeg -v error -y -loop 1 -framerate "$fps" -t 4 -i offline.png -f lavfi -t 4 -i anullsrc=r=48000:cl=stereo \
      -vf "scale=$w:$h:flags=lanczos,format=yuv420p" -c:v libx264 -preset veryslow -tune stillimage \
      -profile:v high -bf 0 -g "$fps" -threads 1 -fflags +bitexact -flags:v +bitexact -flags:a +bitexact \
      -map_metadata -1 -c:a "$codec" -b:a 64k -shortest -movflags +faststart "offline-$name-$a.mp4"
  done
done
