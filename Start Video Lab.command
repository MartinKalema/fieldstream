#!/bin/zsh
cd -- "${0:A:h}"
./lab start || exit $?
open 'http://127.0.0.1:18889/camera-01'
open 'http://127.0.0.1:28889/camera-01'
echo 'Other source links are listed above and in SOURCES.txt.'
echo 'Keep this Mac awake while streaming. Close this window without stopping the lab.'
