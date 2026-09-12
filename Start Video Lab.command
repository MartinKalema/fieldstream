#!/bin/zsh
cd -- "${0:A:h}"
./lab start || exit $?
echo 'Opening the first configured source in GStreamer. Start sending from that device.'
echo 'Keep this Mac awake while streaming. Close the video window or press Ctrl+C to stop viewing.'
echo 'Video services keep running; use Stop Video Lab.command to stop them.'
exec ./lab view local
