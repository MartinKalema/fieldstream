# Automatic picture-delay test

The clock page can estimate delay by reading a changing black-and-white pattern
that the camera films. It reports an approximate interval for each sampled
picture, alongside the existing playback-progress warning. The interval is
**not a verified camera sensor capture time or a guaranteed delay bound**.

Run the helper from the repository root:

```sh
go run ./cmd/clock_check --source camera-01
```

Open the printed address, normally `http://127.0.0.1:19080/`. Wait until both
pictures are advancing, then press **Start 2-minute measurement**. Keep the page
visible and frame **only the large white clock and pattern** with the camera.
Make the pattern large and sharp. Keep the video players and their smaller,
repeated copies of the pattern outside the camera picture.

The test lasts at most 120 seconds. **Stop measurement** ends it sooner. A
hidden page, interrupted browser observation or reconnected viewer ends the run;
start a fresh test afterward. Stopping this test closes its background pattern
reader and hides the pattern. Camera capture, forwarding, local recording and
R2 uploads keep their existing settings. Browser-default video buffering is
unchanged.

To permit another registered source, start with an explicit list:

```sh
go run ./cmd/clock_check --source camera-01 --sources camera-01,camera-02
```

Then `?source=camera-02` selects that source. Without `--sources`, only the
selected `--source` is allowed. The [developer tools guide](developer-tools.md)
explains the local addresses and other commands.

## What the interval means

The page draws a new QR pattern roughly ten times per second. Each pattern
contains a fresh test-session identifier and an increasing number. The page
keeps the actual draw times in memory. It uses the same Mac browser clock to
record when it samples pixels from each received video. It does not rely on
the camera's clock or assume that every pattern lasted exactly 100 milliseconds.

For example, suppose pattern 12 was drawn at 10.00 seconds and replaced at
10.10 seconds. The received video is sampled at 10.45 seconds and contains
pattern 12. The reported interval is approximately **0.35–0.45 seconds**:

- 10.45 − 10.10 = 0.35 seconds from the end of that pattern's draw interval.
- 10.45 − 10.00 = 0.45 seconds from its beginning.

If the pattern has not yet been replaced, the lower endpoint is zero. This
accounts for the time the pattern was held, but software draw time is not the
exact moment light left the screen. Screen refresh, camera exposure, rolling
shutter, scaling and browser display timing add uncertainty outside that
interval. The result describes the pictured marker's approximate age when its
video pixels were sampled. It is not an exact age for every part of the scene
or for the picture now on screen.

Keep the manual filmed-clock comparison as a separate check. A screenshot
containing the direct clock and the two clocks inside the received pictures
lets you compare their numbers yourself. Agreement in one scene does not
establish accuracy for every camera, display or lighting condition.

## Reading current and historical results

**Latest sampled frame** shows the most recent accepted interval. It expires
after one second from sampling. A failed reading or unavailable playback also
removes the current number; an old success is not kept looking current.

**Successful reads** counts accepted readings against all started reads,
including work canceled when a run ends. Completed reads are tracked separately
in diagnostics. Failed reads, decoding timeouts and checks skipped because the
reader was busy or playback was unavailable are not delay values. They are
reported separately and must not be interpreted as zero delay.

The **typical midpoint** is the median of accepted intervals' midpoints from
this run. The **largest sampled upper value** is the largest upper endpoint
among accepted samples. Both are historical summaries of successful samples.
They exclude unreadable pictures and missed sampling opportunities, so they
cannot establish a worst-case delay, reliability percentage or system maximum.
A low successful-read count makes that limitation especially important.

Stopping preserves the run's historical summary while clearing current
measurements. Starting another test clears the old results and creates a new
session. Reloading or closing the page clears its in-memory history. The page
does not save these reports or sampled images to disk.

## Missing, stale and ambiguous patterns

A pattern must match the current session, have the strict expected format and
refer to a number actually drawn during this run. Another test's pattern,
unreadable pixels, an unknown number, invalid timing or a late decoder reply
produces no accepted delay reading. Decoded strings are never opened as links
or executed.

The worker decodes once, masks that code's bounding rectangle, then scans once
more. A detected second readable code makes the result ambiguous, even if both
contain the same text. This cannot prove that no damaged, covered or overlapping
second code exists. Recursive views of the page can contain older codes from
the same session; framing only the direct white target is part of the test
procedure, not an optional convenience.

A frozen image with fresh video timestamps can pass the separate
[picture-progress warning](picture-stall-warning.md). During this optical test,
its still-readable pattern can instead reveal a growing marker age. If the
pattern becomes unreadable, the automatic measurement remains unavailable.

## Work and security limits

Each video is sampled at most twice per second. One background worker handles
one transferred pixel buffer at a time; busy opportunities are skipped rather
than queued. Images are reduced to at most 960 × 540 pixels. Each request has
at most two decoder calls and a 250-millisecond acceptance deadline. The page
terminates a worker that times out. Browser suspension can delay that timer's
execution, so late results are rejected as well. Repeated timeouts end the test.

These limits bound the work but do not make it free. Drawing patterns, copying
video pixels and decoding them use processor time and can affect playback on a
busy computer. Keep other reader and workload settings the same when comparing
runs. The sampling rate can miss short events between readings.

All code and pattern reading run locally. Video connection setup keeps the
[shared local reader restrictions](picture-stall-warning.md#local-connection-boundary).
The test identifier is an optical correlation value, not a camera credential.
No image is sent to an external QR service.

The encoder uses pinned [qrcode](https://github.com/soldair/node-qrcode) 1.5.4
with dijkstrajs 1.0.3; the decoder uses pinned
[jsQR](https://github.com/cozmo/jsQR) 1.4.0. The
[vendor guide and hash manifest](../cmd/clock_check/assets/vendor/README.md)
explain the reproducible build. The encoder's MIT licenses and the decoder's
Apache 2.0 license are retained alongside the local files.

## Checks and current validation

```sh
go test ./cmd/clock_check ./internal/viewer
node --test cmd/clock_check/*test.mjs
node scripts/qr-tools/vendor-clock-qr.cjs --check
```

The vendor check needs the already pinned dependencies installed under
`scripts/qr-tools`; the built clock helper does not need npm or Node. The
[isolated browser fixture](../cmd/clock_check/testdata/fixture/README.md) exercises
the production page with generated video, without connecting to a camera or R2.

On 12 September 2026, the production page was exercised in an actual browser
through the fixture's real WebRTC connections. These observations concern
generated browser video:

| Controlled case | Observed result |
| --- | --- |
| Baseline generated marker | Approximate readings around 0–0.13 seconds |
| Marker delayed intentionally by one second | Approximate readings around 0.97–1.07 seconds |
| Marker held while video timestamps kept advancing | Reported marker age grew beyond 20 seconds, while the playback-progress check still showed advancing video |
| Unreadable marker or marker from another session | The current measurement was cleared instead of retaining the previous value |

After the final lifecycle and read-count changes, a repeated baseline check
showed 47 successful reads out of 47 started reads for each picture, with
historical upper endpoints reaching approximately 0.16 seconds. The two-marker
fixture then produced no new accepted forwarded readings. The browser reported
that picture as unreadable; the separate decoder tests exercise explicit
two-code ambiguity with readable source pixels.

The final lifecycle and accounting behavior is covered by tests, including
interrupted observations, late results, a fresh run after reconnecting and
started work that is canceled before completion. Those tests check the stated
rules; they do not add physical-camera accuracy evidence.

The physical-camera manual cross-check remains pending. At the latest check,
the camera still showed the keyboard rather than the direct white target, so
no valid physical comparison could be made. The fixture readings are not exact
bounds and do not establish camera-to-screen accuracy, a system maximum or
performance under every browser workload. A later physical check should record
the setup, valid and failed readings, and the simultaneous filmed-clock result.
