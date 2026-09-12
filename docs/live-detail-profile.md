# Try compression during live forwarding

The `detail` profile keeps the camera's pixel dimensions, reduces the output to
20 frames per second and targets 1,200 kilobits per second. A 720p camera stays
720p. This offers an option between forwarding the original compressed video
and reducing its picture size to 360p.

The [prepared-video bandwidth tests](bandwidth-results.md) justify a live trial:
the 720p candidate delivered all scored pictures at a 2,000 kbps connection cap,
but lost pictures at 1,500 kbps. They did not measure live encoding or total
camera-to-screen delay. Compression requires processing, so a smaller stream
does not necessarily display sooner on a connection with ample capacity.
The [first physical-camera results](live-detail-results.md) record the measured
CPU, payload, browser stalls and picture-age trade-off, including a late stall
that remained unexplained.

## Select or undo the setting

Run from the project folder with the updated controller running. Building a
replacement executable does not update an existing controller process. For an
upgrade, finish the current capture, run `./lab stop`, then `./lab build` and
`./lab start` before selecting this profile. A whole-lab restart interrupts all
sources; it is only needed for the upgrade, not for later profile switches.

```sh
./lab --source camera-01 profile detail
./lab --source camera-01 status
```

Only the selected source's forwarder reconnects. Its incoming camera video,
original recording and other sources keep their own settings. Audio remains
excluded, as in the other forwarding profiles. Switching profiles does not
change the camera's settings or introduce a bandwidth limit on the connection.
The saving applies between the forwarding programs. Camera-to-Mac traffic,
original recording sizes and their R2 uploads are not reduced by this profile.

Restore forwarding without another video encode with:

```sh
./lab --source camera-01 profile copy
```

Settings are saved per source and survive controller restart. The existing
`small` profile remains available for a 640 × 360 picture at 20 fps.

## Why these settings

| Setting | Reason and cost |
| --- | --- |
| Keep incoming pixel dimensions | Avoid discarding small detail through resizing. Compression can still make text harder to read. |
| 20 fps | Process and send fewer pictures than a 30 fps source. Motion becomes less smooth. |
| H.264, `veryfast` | Match the tested compression candidate and compatible viewing format. Faster encoding trades compression efficiency for CPU time. |
| `zerolatency`, no B-frames | Avoid encoder features that hold pictures while waiting for future pictures. This is not a zero-delay guarantee. |
| Fixed 20-frame keyframe interval | Send a complete refresh picture about once per second at 20 fps. Large refresh pictures still produce bursts. |
| 1,200 kbps target and maximum, 600 kbit encoder buffer | Match the tested rate-control budget. The buffer is an encoder rate-control limit, not a promise of a fixed half-second playback delay. |
| One decoder thread, one filter thread, two encoder threads | Bound processing parallelism and avoid extra frame-thread decoding delay. This choice needs capacity checks above 720p and with several simultaneous encoders. |

FFmpeg documents that frame-thread decoding can add picture delay; placing the
decoder thread option before the input applies it to decoding. Encoder thread
options appear after the input. [FFmpeg's thread documentation](https://www.ffmpeg.org/doxygen/8.0/structAVCodecContext.html).
The existing input connection, timeouts and initial stream inspection are kept.

The authenticated connection between programs on this Mac and encrypted SRT
forwarding remain available. A profile change preserves the selected connection
type and its saved recovery allowance. Keeping pixel dimensions is not a claim
that every incoming resolution will fit this CPU or bitrate budget.

## Measure the live cost

Keep the same camera settings, scene, lighting, connection and viewer load for
each run. Let a profile switch settle before observing it. Compare `copy` and
`detail` in separate windows, then repeat them in the opposite order when
possible. Do not compile or run media acceptance tests during measurements.

```sh
go build -o .tools/bin/relay_check ./cmd/relay_check
.tools/bin/relay_check --root . --source camera-01 --duration 30s
```

This reports the forwarder's CPU, average and maximum sampled memory, and the
payload rate received at the forwarded video service. One fully occupied CPU
core is 100%; two cores can be 200%. It excludes the camera, receiver, recorder,
uploader and viewer processes. Payload rate excludes some network overhead.
The private JSON report is saved under ignored `reports/relay-check-*`.

A process restart, missing observation, stale status, changed publisher or
backwards counter makes the entire observation inconclusive. It cannot quietly
join measurements across a reconnect. See [relay_check's measurement limits](../cmd/relay_check/README.md).

Use two separate observations alongside that command:

1. Open the [filmed-clock page](developer-tools.md#filmed-clock-comparison) and
   point the camera at its clock. Open local and forwarded GStreamer windows
   beside the target and capture all three clocks together. Subtract
   the time inside each video from the large clock to estimate picture age.
   Take at least three readings per profile. Screen refresh, camera exposure
   and frame selection limit precision.
2. Watch each native picture during still scenes and movement. Record visible
   blocks and pauses, including when they happened. Keep viewer settings and
   the number of open windows the same across `copy` and `detail` runs.
   The removed browser tool's frame and freeze counters are not available in
   this native viewer. Manual observations do not replace those counters;
   quantitative native playback measurements remain to be implemented.

For the first 720p local trial, the provisional targets are at least 19 decoded
frames per second over each settled 30-second detail window, no reported
freezes where the counter is available, average forwarding CPU below one full
core, and a forwarded clock no more than about 100 ms behind the local picture
in each screenshot. These are trial decisions, not production requirements or
proven maximums. A smaller payload is useful only if the resulting picture is
still useful. The camera's original frame rate is a separate baseline.

## Connection to the design

The forwarded picture is a derived version of the incoming video. Its resolution,
frame rate and compression can change while the original recording remains
separate. This applies the separation between original data and derived views
discussed in *Designing Data-Intensive Applications*. Independent processes limit
which work restarts during a profile change; they still share this Mac's CPU,
disk, network and power.

The profile is selected manually. Automatic adaptation needs reliable capacity
signals and rules that prevent repeated switching. The current short local
measurements do not establish those rules, poor-network reliability or
four-camera capacity.

## Verification

On 12 September 2026, the full media acceptance passed all 100 assertions. It
decoded the detail output at 1280 × 720 and 20 fps over both local and encrypted
SRT forwarding. During profile changes, camera-01's original capture and
recorder processes stayed running, as did camera-02's workers and delivery.
All 54 finalized generated recordings decoded fully. Orderly shutdown and
restart preserved their metadata and checksums; test cleanup reported no errors.
[Private integration report](../reports/integration-live-detail-profile.json).

Go package tests, `go vet ./...`, the browser metric tests, and focused
relay_check race tests passed. The measurement tests cover unavailable counters,
counter resets, process identity changes and cancellation of nested diagnostic
processes. These checks do not qualify a physical camera or a real connection.
