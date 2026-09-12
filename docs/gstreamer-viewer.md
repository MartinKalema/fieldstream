# Compare a GStreamer viewer with the browser

GStreamer is a toolkit for receiving, processing and displaying video. This
experiment opens the existing camera picture in a separate desktop window. It
tests whether a different way of receiving and displaying the picture can reduce
delay. A working window alone does not establish that it is faster.

The Go command starts and stops the experiment. GStreamer handles the video.
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
With `gst-launch-1.0` already installed and available in your shell:

```sh
go run ./cmd/gstreamer_check --root . --source camera-01 --duration 120s
```

This workspace also has a private GStreamer 1.28.7 runtime. Use its wrapper:

```sh
go run ./cmd/gstreamer_check --root . --source camera-01 --duration 120s \
  --gst-launch .tools/gstreamer-1.28.7/gst-launch-1.0
```

The default is the local picture. Add `--route forwarded` to read the forwarded
picture. The source must already be registered in this lab; replace `camera-01`
when testing another device.

The command stops after the requested time. Durations must be between 5 and 120
seconds; the default is 30 seconds. Press Ctrl+C in the terminal to stop early.
Stopping this viewer does not stop the camera or the normal browser players.

Useful options:

| Option | Meaning |
| --- | --- |
| `--latency-ms 20` | Request 20 milliseconds of receiver waiting; this is the default |
| `--decoder software` | Decode using the computer's processor; this is the default |
| `--decoder hardware` | Request Apple's hardware video decoder |
| `--sink headless` | Decode and discard pictures without opening a window |
| `--dry-run` | Print the exact program arguments without starting a viewer or saving a report |

The allowed receiver waiting values are 0–200 milliseconds. Start with the
default. Change one setting at a time so a later result has a clear comparison.

## What the experiment changes

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
Our launcher explicitly requests **20 milliseconds** and enables
`drop-on-latency`, which limits that component's packet buffer. Neither setting
limits the entire picture's age. TCP, the decoder and the display can still
introduce waiting. [GStreamer RTSP source controls](https://gstreamer.freedesktop.org/documentation/rtsp/rtspsrc.html)

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
   forwarding profile. Open the existing clock comparison page on the Mac.
2. Start a fresh GStreamer run of up to 120 seconds. Compare its local route
   with the browser's local picture, or its forwarded route with the browser's
   forwarded picture.
3. Point the camera at only the white clock target. Keep the desktop viewer
   beside the browser without covering that target. Allow playback to settle.
4. Take several screenshots containing the direct clock, the browser's filmed
   clock and the GStreamer window's filmed clock. For each picture, subtract
   its filmed number from the direct clock number in that same screenshot.
5. Record unreadable clocks and any pauses as well as successful readings.
   Repeat the run before changing settings. Then change only one option, such
   as the decoder, and repeat under the same conditions.

For example, a direct clock of 40.00 seconds and a filmed clock of 39.70 seconds
give an approximate picture age of 0.30 seconds. Exposure, screen refresh,
frame timing and screenshot capture limit this method's precision. A few
screenshots cannot establish a guaranteed maximum delay.

The browser uses WebRTC, the video connection method also used in video calls.
The desktop experiment uses RTSP over TCP. A difference therefore compares
**two complete viewer routes**; it does not isolate the window or decoder alone.
Keep the number of open viewers and other computer work similar across runs.

The [automatic clock-pattern measurement](automatic-picture-delay.md) samples
the browser's own videos. It does **not** read the separate GStreamer window.
Use manual screenshots for the desktop comparison. Headless decoding and
measurements taken before display cannot establish camera-to-screen delay.

## What the saved report proves

Each real run saves a private directory under `reports/gstreamer-check-*`.
It contains the selected options, exact arguments, process start and stop
information, and a log. The report and log files have private permissions and
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
offline when the live comparison was attempted, so the physical
browser-versus-desktop comparison remains pending. No speed improvement is
claimed yet.
