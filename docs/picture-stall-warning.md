# Warnings when picture progress stops

The clock page watches its local and forwarded players separately. If one stops
presenting advancing video timestamps, it shows a warning even when the video
connection remains open. **Picture advancing does not mean the camera picture
is recent.** Keep using the filmed clock to measure camera-to-screen delay.

From the repository root:

```sh
go run ./cmd/clock_check --source camera-01
```

Open the printed address, normally `http://127.0.0.1:19080/`. The page opens two
video readers for the selected source. Each is a native browser video player,
with its own playback warning and **Reconnect this viewer** button.

To permit another source in the same helper, list it explicitly:

```sh
go run ./cmd/clock_check --source camera-01 --sources camera-01,camera-02
```

Then `http://127.0.0.1:19080/?source=camera-02` selects that allowed source.
Without `--sources`, only the selected `--source` is allowed. The helper accepts
up to four distinct source IDs. Opening a source page does not register a camera
or change its sender settings.

## Reading the warning

| What the page observes | What it reports |
| --- | --- |
| Video presentation timestamps keep advancing | Picture advancing; camera age remains unverified |
| No advancing picture timestamp for 1.5 seconds while observation is available | Picture progress has stopped |
| Pictures resume after an interruption | Checking recovery for one second of sustained progress |
| The native player is paused | Playback paused |
| The browser reports the page hidden | Picture checking is paused |
| The needed callback is unsupported, or observation/connection is unavailable | The check is unavailable, with its reason |
| The video track or player ends | Video has ended |

The warning is checked about four times per second. The 1.5-second threshold is
an observation rule, not a guaranteed alarm deadline. A suspended browser or a
busy page cannot update its warning until it runs again. Long gaps in observation
start a fresh check; they do not count as evidence of continuous playback or
successful recovery.

The page keeps the latest 12 playback state changes in memory. A successful
viewer reconnect keeps that page's previous interruption visible. Reloading or
closing the page clears the history; it is not a saved incident log.

Use **Reconnect this viewer** to close and reopen just that picture's browser
read session. It can help when that reader is stuck, but does not prove or repair
an upstream fault. This tool does not change camera capture, forwarding,
recording or R2 uploads. It preserves browser-default video buffering. The
separate [browser buffer experiment](../cmd/browser_check/README.md) is where
buffer settings are compared.

## Why watch the browser

A service can answer a status request while an old picture remains on screen.
A connection can stay open while no new video arrives. Those checks are useful,
but they describe different parts of the system. Watching each player adds an
observation close to what the person is actually seeing.

The page uses `requestVideoFrameCallback()`, the browser's notification that a
video frame has been submitted for display. It checks that both the presentation
count and the video's own timestamp advance. This is a browser observation,
not a measurement at the camera sensor or proof that every frame reached the
screen. The callback can be delayed by browser scheduling. See the
[video frame callback definitions](https://wicg.github.io/video-rvfc/).

When available, the receiver's `framesDecoded` and `bytesReceived` counters help
explain a stopped picture. For example, received bytes can increase without new
pictures being displayed. Missing counters remain unavailable; they are not
reported as zero. These counters describe the receiving browser, not capture
time. See the [WebRTC statistics definitions](https://www.w3.org/TR/webrtc-stats/).

We do not compare pixels to decide whether video is alive. A camera can be
working normally while filming a motionless wall. Conversely, a failed camera
can keep sending the same image with new timestamps. That second case can pass
this progress check. A steadily advancing stream can also remain several seconds
behind reality.

Our application of the DDIA discussion of unreliable networks and clocks is to
keep these claims separate: **what this observer measured**, **whether a
connection is open**, and **when the distant camera captured the picture**.
An observation at one component cannot guarantee the state of every other
component. This is a design interpretation, not a quotation or a claim that the
book prescribes this video implementation. The filmed clock stays explicit
because the browser's video timeline is not a verified original-camera clock.

## Local connection boundary

The Go helper serves embedded page assets and shares a restricted WHEP proxy
with the browser-buffer tool. WHEP is the HTTP exchange that sets up a WebRTC
video read connection; WebRTC is the browser's live-media connection. This proxy
supports read setup and the follow-up messages needed to maintain or close that
reader. It has no route for publishing camera video or controlling the lab.

The helper binds only to a loopback address, which means this computer. Its
outgoing setup requests use only `127.0.0.1` and the two ports selected when the
helper starts: 18889 locally and 28889 after forwarding by default. Browser
requests cannot supply another destination, port or unlisted source. The helper
checks the browser Host and Origin, limits setup requests and responses to
256 KiB, caps upstream request time at eight seconds and does not follow
redirects. Browser Authorization, Cookie and Origin headers are not passed to
the receiver. Its client cookie jar is disabled as well.

This remains a diagnostic for a trusted local computer. It does not add remote
viewer accounts or Internet access. Opening extra pages adds video readers and
browser decoding work, so keep reader counts consistent when comparing delay or
processor use.

## Check the implementation

```sh
go test ./cmd/clock_check ./internal/viewer ./cmd/browser_check
node --test cmd/clock_check/watch_test.mjs
node --test cmd/clock_check/app_test.mjs
node --test cmd/browser_check/metrics_test.mjs
node --check cmd/clock_check/assets/app.mjs
```

The watch tests exercise progress, interruptions, recovery and unavailable
observations. The application tests run the production script with controlled
browser APIs and promises: a hung statistics request cannot create concurrent
reads across reconnects, and an old reply cannot populate a replacement viewer.
The Go tests cover the local HTTP boundary and embedded assets.
They do not establish behavior in every browser or explain a physical-camera
stall.

The [isolated browser fixture](../cmd/clock_check/testdata/fixture/README.md) uses
the production page and monitor with real in-page WebRTC peers and generated
canvas video. Its controls hold frames while keeping those peer connections
open, then resume or end them. It does not contact the live camera or WHEP
services. Its tests are under `testdata`, so run them explicitly:

```sh
go test ./cmd/clock_check/testdata/fixture
go vet ./cmd/clock_check/testdata/fixture
```

A real-browser fixture run and a live-reader check are separate validation steps.
Neither alone establishes total camera age, long-term availability or a maximum
warning delay.

## Checked in the browser on 12 September 2026

The isolated fixture ran in the Codex in-app browser using real WebRTC peers,
video presentation callbacks and receiver statistics:

| Controlled action | Observed result |
| --- | --- |
| Hold only forwarded frames | Forwarded warning; local picture kept advancing; both connections stayed open |
| Resume forwarded frames | Checking recovery, then picture advancing; earlier interruption retained |
| Hold both sources | Independent warnings on both players; both connections stayed open |
| End the forwarded connection | Check unavailable; local picture continued |
| Reconnect the forwarded viewer | Fresh picture observations, then advancing; previous interruption retained |
| Pause the forwarded player while its source kept sending | Playback paused, with its connection still open |
| Resume the paused player | Checking recovery before the advancing label returned |

The normal comparison page also opened real local and forwarded camera streams
through the shared WHEP proxy. Both reached **Picture advancing** after the user
restarted broadcasting. Only the separate clock-page helper was replaced; the
lab controller, relay and uploader were not restarted.

Hidden-page handling, missing callbacks, timestamp resets and exact threshold
boundaries were checked with controlled unit inputs, not demonstrated across
all browsers. This run verifies the listed cases; it does not identify the cause
of the earlier six-second camera delay or prove that all stale pictures can be
detected. Private observations are kept in
`reports/picture-stall-warning-20260912.json` and are excluded from Git.
