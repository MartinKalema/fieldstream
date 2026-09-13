# GStreamer live viewer and comparison tool

GStreamer is a toolkit for receiving, processing and displaying video. It is now
the normal live viewer for this lab. It opens the existing camera picture in a
separate desktop window. The old live browser players have been removed. A
standalone white clock remains available for manual delay readings; the
saved-video compression page remains a separate tool. A working window alone
does not establish that it is faster.

The Go command starts and stops the viewer. GStreamer handles the video.
Each run reads an existing source and leaves camera, forwarding, recording and
upload settings unchanged.

The camera remains on a separate device, such as the broadcasting phone or
tablet. GStreamer runs on the viewing Mac. The full test route is:

```text
External camera device → network → Mac's existing receiver → GStreamer desktop window
```

The viewer's loopback address selects the receiver on the Mac, not a camera on
the Mac. Generated pictures are useful for checking installation, but they do
not test the physical camera or its network connection. The current receiving
and forwarding services also share one Mac; a separate receiving station is a
later experiment requiring another machine.

## Open a viewer

Run these commands from the project folder while the lab and camera are running.

```sh
./lab build
./lab view
```

The normal viewer selects the first registered source, its local picture,
software decoding and a **50 ms** receiver wait. It has no two-minute limit.
Close its video window or press Ctrl+C in its terminal to stop only the viewer.
The receiving, forwarding, recording and upload services keep running.

```sh
./lab --source camera-02 view local
./lab --source camera-01 view forwarded
./lab --source camera-01 view local --latency-ms 100
./lab view --dry-run
```

Place `--source` before `view`; its other options follow `view`. The desktop
**Start Video Lab.command** launcher starts the services and opens the first
configured source. `./lab start` alone starts services without opening a window.
When the receiving service is running but the camera is absent, the command
waits up to a minute before opening GStreamer; Ctrl+C cancels that wait. It
reports a stopped or unavailable receiving service promptly. If the camera
does not arrive in time, start it and reopen the viewer. A connection failure
after playback starts can also end the viewer. Automatic reconnection is not
implemented here.

Both viewer commands first use an explicit `--gst-launch` path when supplied,
otherwise this project's `.tools/gstreamer-1.28.7/gst-launch-1.0`, then an
installation found in the shell's PATH. A broken private selection fails
clearly instead of silently choosing a different runtime. On a fresh Mac,
run `scripts/install-gstreamer-runtime.sh` once to install the private runtime.
No installation happens when opening a viewer.

Each command opens one window. The current window title is "OpenGL renderer";
the terminal identifies its source and route. This viewer has no
picture-progress warning or automatic clock measurement. The retired browser
features have not been transferred to GStreamer.

It also does not provide user login or role-based access control (RBAC).
Current playback trusts programs on this Mac. The future interface must check
which user may watch or control each camera, and the server must enforce those
permissions on actual video and control requests. See the
[viewer and interface decision](decisions/003-gstreamer-live-viewer.md).

## Run a timed comparison

The separate diagnostic keeps the **100 ms reference setting** from the earlier
comparisons. Add `--latency-ms 50` to test the normal viewer's current setting:

```sh
go run ./cmd/gstreamer_check --root . --source camera-01 --duration 120s
```

To select an explicit runtime wrapper:

```sh
go run ./cmd/gstreamer_check --root . --source camera-01 --duration 120s \
  --gst-launch .tools/gstreamer-1.28.7/gst-launch-1.0
```

The default is the local picture. Add `--route forwarded` to read the forwarded
picture. The source must already be registered in this lab; replace `camera-01`
when testing another device.

The command stops after the requested time. Durations must be between 5 and 120
seconds; the default is 30 seconds. Press Ctrl+C in the terminal to stop early.
Stopping this viewer does not stop the camera, other native viewers or the lab.

Useful options:

| Option | Meaning |
| --- | --- |
| `--latency-ms 100` | Request 100 milliseconds of receiver waiting; default for the diagnostic, while normal `view` defaults to 50 |
| `--decoder software` | Decode using the computer's processor; this is the default |
| `--decoder hardware` | Request Apple's hardware video decoder |
| `--sink headless` | Decode and discard pictures without opening a window |
| `--dry-run` | Print the exact program arguments without starting a viewer or saving a report |

The allowed receiver waiting values are 0–200 milliseconds. Start with the
default. Change one setting at a time so a later result has a clear comparison.

## How the viewer works

The route through the Mac is:

```text
Existing receiver → receive video → decode pictures → keep latest waiting picture → desktop window
```

**Receiving the video.** The viewer uses RTSP, a way to request a video stream,
over TCP, a connection that delivers data in order and retries missing data.
The addresses are fixed to this Mac: local video uses
`rtsp://127.0.0.1:18554/<source>` and forwarded video uses port `28554`.
The command accepts a registered source name, not an arbitrary video address.
It does not need the camera's publishing password.

GStreamer's `rtspsrc` component normally allows **2,000 milliseconds** of waiting.
Normal viewing explicitly requests **50 milliseconds** (the diagnostic defaults
to **100 milliseconds**) and enables
`drop-on-latency`, which limits that component's packet buffer. Neither setting
limits the entire picture's age. TCP, the decoder and the display can still
introduce waiting. [GStreamer RTSP source controls](https://gstreamer.freedesktop.org/documentation/rtsp/rtspsrc.html)

The first physical-camera trial used 20 milliseconds and produced broken blocks
during movement. Repeating the movement with only this setting changed to
100 milliseconds removed the visible problem according to the observer. The
diagnostic default was raised accordingly. A later 50 ms trial stayed clear
according to the observer, so normal viewing now starts at 50 ms with 100 ms
available if damage returns. A small buffer can throw away video data that
arrives in bursts even though the two receiving programs share a computer. This
is a likely explanation for the observation, not a measured packet-drop result;
the initial logs did not include detailed packet-eviction tracing.

**Decoding the pictures.** The software setting uses `avdec_h264` with one worker
thread. This provides an explicit starting point instead of letting the decoder
choose its thread count. That single thread must keep up with the incoming
pictures; larger video may need a different choice. [Software decoder controls](https://gstreamer.freedesktop.org/documentation/libav/avdec_h264.html)

The hardware setting uses `vtdec_hw`, Apple's hardware-only decoder through
GStreamer. It is a separate experiment: lower processor use does not prove lower
picture delay. It may fail if the required component or support for the video's
format is unavailable. The similarly named `vtdec` can choose hardware or
software, so it is not the command's hardware-only option. [Hardware-only decoder](https://gstreamer.freedesktop.org/documentation/applemedia/vtdec_hw.html),
[automatic Apple decoder](https://gstreamer.freedesktop.org/documentation/applemedia/vtdec.html)

**Keeping pictures from piling up before display.** After decoding, a queue
holds at most one waiting picture. If a newer picture arrives while that queue
is full, the old waiting picture is discarded. The setting is
`max-size-buffers=1`, with the byte and time limits disabled and
`leaky=downstream` selecting old pictures for removal. This can skip pictures
when the display cannot keep up. [Queue controls](https://gstreamer.freedesktop.org/documentation/coreelements/queue.html)

The queue is after decoding because later compressed pictures can depend on
earlier ones. Removing arbitrary compressed pieces before decoding can damage
the pictures that follow. One waiting decoded picture does not mean only one
picture exists in the whole pipeline: the decoder, window and other components
have their own work and storage.

**Displaying pictures promptly.** The window uses `glimagesink`, an OpenGL video
display component supported on macOS. Its `sync=false` setting asks it to show
arriving pictures without waiting for their scheduled presentation times. This
can make movement uneven. It also disables the base sink's normal
`max-lateness` frame-dropping rule; the queue described above performs the
explicit old-picture removal. Screen refresh and window drawing still take
time. [Platform display options](https://gstreamer.freedesktop.org/documentation/tutorials/basic/platform-specific-elements.html),
[display timing rules](https://gstreamer.freedesktop.org/documentation/base/gstbasesink.html)

## Make a fair comparison

1. Keep the same camera connection, picture size, frame rate, lighting and
   forwarding profile. Run `go run ./cmd/clock_check` and open its printed local
   address, normally `http://127.0.0.1:19080/`. This page displays only a clock.
2. Start a fresh GStreamer diagnostic of up to 120 seconds for the chosen source
   and route. Use 100 ms as a reference, then repeat at 50 ms with all other
   settings unchanged. Use normal `./lab view` for a longer run.
3. Point the camera at only the white clock target. Keep the native window
   beside the clock without covering it. Keep the page visible and allow
   playback to settle.
4. Take several screenshots containing both the direct clock and the first
   filmed clock inside the GStreamer window. Subtract the filmed number from
   the direct clock number in that same screenshot. Ignore smaller repeated
   copies of the clock inside the camera picture.
5. Record unreadable clocks and any pauses as well as successful readings.
   Repeat the run before changing settings. Then change only one option, such
   as the decoder, and repeat under the same conditions.

For example, a direct clock of 40.00 seconds and a filmed clock of 39.70 seconds
give an approximate picture age of 0.30 seconds. Exposure, screen refresh,
frame timing and screenshot capture limit this method's precision. A few
screenshots cannot establish a guaranteed maximum delay.

Keep the selected route, number of open native viewers and other computer work
similar across runs. For a local-versus-forwarded comparison, two native
windows can read the same camera through its two routes. Match each window to
the command that opened it; both currently have the generic OpenGL title.
Independent viewers can show different nearby frames, so one snapshot cannot
isolate the time spent forwarding.

The removed [automatic pattern experiment](automatic-picture-delay.md) sampled
only its browser players. It never read the separate GStreamer window. Current
native readings are manual. Headless decoding and measurements taken before
display cannot establish camera-to-screen delay.

## What the saved report proves

Each diagnostic run saves a private directory under `reports/gstreamer-check-*`;
normal viewing uses `reports/live-view-*`.
Both record the selected options, process start and stop information, and a log.
The diagnostic also saves its exact program arguments. The report and log files have private permissions and
are ignored by Git.

The log keeps at most 1 MiB. Additional output is counted and discarded while
still being drained, so a full log does not block the viewer. A noisy run may
therefore have an incomplete saved log.

A process reaching its requested stopping time is not proof that a window
displayed useful pictures throughout that time. Headless mode deliberately
discards decoded pictures. Neither process lifetime nor a successful headless
run is a picture-delay measurement. Keep those checks separate from the filmed
clock results.

## Runtime used on this Mac

For this experiment, the official **GStreamer 1.28.7 macOS universal runtime**
was downloaded from the [GStreamer download page](https://gstreamer.freedesktop.org/download/).
The downloaded package's SHA-256 was verified as:

```text
529fdf4a4027d942e59b5b3564f6400adaa008f63ce5f3fed4ffe35d73911994
```

For a fresh macOS checkout, the reproducible setup command is:

```sh
scripts/install-gstreamer-runtime.sh
```

It refuses an existing output directory. An optional new output directory and
existing downloaded package can be supplied as its first and second arguments.
The package must match the pinned hash above. It needs roughly 2 GB of temporary
free space during extraction; the installed runtime uses about 674 MB on this
Mac. The package contains both Apple Silicon and Intel binaries.

The package was extracted into the ignored `.tools/gstreamer-1.28.7` directory.
Installer scripts were not run, Python binding payloads were not merged into
the private runtime, and existing Homebrew packages were not changed. The local
wrapper selects this private runtime for the diagnostic. Its initial scan is
limited to 14 viewer and generated-test components, with its own plugin cache.
The downloaded runtime files are not part of the repository. Another checkout
can run the setup script, use its own installation, or supply an explicit
`--gst-launch` path.

## Results

On 12 September 2026, the private 1.28.7 runtime passed these checks on the
development Mac:

- All required viewer components were available, including both decoders.
- A generated 720p/30 fps input of 90 pictures completed encoding and software
  decoding without errors.
- A separate 90-picture, generated 720p/30 fps input completed encoding and
  hardware decoding without errors.
- A 450-picture generated display test created a Cocoa display context, entered
  playback and completed after approximately 15 seconds without errors. This
  was a process-level display check; the desktop window was not independently
  inspected or measured.
- With the physical camera disconnected, the real read-only RTSP request
  received a 404 response. The diagnostic correctly reported an early failure,
  rather than a successful timed run.

These generated tests establish basic installation and component compatibility.
They do not establish physical camera-to-screen delay. The external camera was
initially offline when the live comparison was attempted.

### Historical physical camera trial on 13 September 2026

The external iPad resumed broadcasting 1280 × 720 H.264 video. The observer
confirmed the GStreamer desktop window displayed it while both browser players
also advanced. The existing camera allowance remained 80 ms and the forwarding
profile remained `copy`.

| Viewer setting | Observation |
| --- | --- |
| 20 ms, software decoder | The observer reported broken blocks and smears during movement in GStreamer only, and supplied a screenshot. The two-minute process still exited normally. |
| 100 ms, same decoder and other pipeline settings | The observer repeated the movement and reported that the broken blocks were gone. The two-minute process exited normally. |

These were sequential observations, not identical replayed movement. The only
viewer media setting changed was its receiver allowance; the second run also
enabled diagnostic logging. The initial log was not detailed enough to measure
packet evictions. A sampled camera receiver check showed no SRT loss or drops
and no input frame errors, but that snapshot cannot exclude an earlier event.

The 100 ms setting remains the reference for the timed diagnostic. The
one-picture decoded queue and camera settings are unchanged. This short trial
does not establish long-term image reliability.
Private evidence is saved in `reports/gstreamer-motion-quality-20260913.json`.

### Historical simultaneous filmed-clock comparison

The observer supplied two screenshots from a further 120-second run using the
100 ms setting, software decoder and local route. Each image contains the
direct clock, the native GStreamer window and both browser pictures. The table
uses the first filmed white clock in each picture, excluding recursive copies.
Two independent readings agreed on the digits. The browser players in these
screenshots have since been removed; this table preserves the original results.

| Screenshot time | Direct clock | GStreamer filmed clock → delay | Browser local filmed clock → delay | Browser forwarded filmed clock → delay |
| --- | --- | --- | --- | --- |
| 02:07:05 | 655.78 | 655.35 → **0.43 s** | 655.35 → **0.43 s** | 655.31 → **0.47 s** |
| 02:07:31 | 680.84 | 680.62 → **0.22 s** | 680.48 → **0.36 s** | 680.52 → **0.32 s** |

For example, 680.84 − 680.62 gives approximately 0.22 seconds for GStreamer.
Its picture matched the local browser's age in the first screenshot and was
approximately **0.14 seconds newer** in the second. This shows a faster moment,
not a consistent improvement. Two samples cannot establish an average, a
maximum or dependable performance. Exposure, screen refresh, frame timing and
screenshot capture limit the precision.

The second forwarded browser picture was slightly newer than the local browser
picture. These players present video independently; the difference does not
mean that forwarding took negative time. GStreamer and the browser also use
different receiving methods: the browser used WebRTC and GStreamer used RTSP
over TCP. The result therefore compares complete routes and does not isolate
decoding or the window alone.

The Mac's hardware decoder remains a possible controlled comparison with the
same wait and camera settings, checking repeated delay readings and movement
quality. The software setting remains the default. Private readings and screenshot references are in
`reports/gstreamer-clock-comparison-20260913.json`.

### Follow-up at 50 ms

The observer then tried a 120-second diagnostic with the same local route and
software decoder, changing the receiver wait to 50 ms. During the movement test,
they reported "no blocks now". That run ended at its time limit with exit code 0.
Its private process report is `reports/gstreamer-check-1993220736/report.json`.
Normal viewing now starts at 50 ms; use 100 ms if the broken picture returns.

At 02:56:07 on 13 September 2026, the observer supplied the first filmed-clock
screenshot from normal viewing at 50 ms, using the local route and software
decoder. The direct clock reads **202.23** and the first filmed clock appears
to read **201.96**, giving approximately **0.27 seconds** of camera-to-screen
delay. Two independent readings agreed, but the final filmed digit is ghosted;
this is not a measurement precise to one hundredth of a second. Camera exposure,
screen refresh and screenshot timing also add uncertainty.

This is one observation, not an average or a maximum. It was taken at a different
moment from the 100 ms trials, so it does not isolate the effect of changing the
buffer. The configured wait is only part of total picture delay. Longer runs,
difficult connections and repeated readings still need testing. Private evidence
is saved in `reports/gstreamer-clock-50ms-20260913.json`.

### Normal-viewer checks and remaining work

Before the old browser code was removed, Go tests, vet and race checks passed.
A normal live viewer remained open for approximately 138 seconds, beyond the
diagnostic's 120-second limit. That verified the persistent command's lifetime;
it did not measure picture age throughout the run.

After removal on 13 September 2026, all Go tests and vet passed, along with
focused race checks and the saved-video page tests. The full `./lab test` run
passed **100 of 100 checks** and fully decoded **52 finalized recordings**. It
covered two generated sources, rejected publishing credentials, forwarding
interruptions and crashes, a paused receiver, picture profiles, and recording
persistence across restart. There were no cleanup errors. Private evidence:
`reports/integration-browser-removal-20260913.json`.

The live camera and forwarding returned after the test. Saved source settings
and controls were byte-for-byte unchanged; the browser video ports were closed.
The new plain clock was inspected in the browser and its digits advanced.
The local native pipeline entered playback, and a five-second forwarded
headless GStreamer run ended normally. These startup and process checks do not
establish visual quality or screen delay after the change.

Longer viewing runs, poor connections and repeated end-to-end clock readings at
50 ms are still pending. Native picture-progress warnings, automatic delay
measurement, automatic reconnection and per-user permissions are not implemented.
