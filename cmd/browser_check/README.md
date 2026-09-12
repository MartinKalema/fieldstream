# Browser buffer experiment

This optional diagnostic leaves the lab controller, camera settings, recordings,
R2 and the existing clock page unchanged. It serves a separate page on loopback.
It creates no media readers until **Start comparison** is pressed.

From the project directory:

```sh
go build -o .local/diagnostics/browser_check ./cmd/browser_check
.local/diagnostics/browser_check --root . --sources camera-01,camera-02
```

Open `http://127.0.0.1:19081/?source=camera-01&route=local`. The source ID must be
in the command's explicit allowlist. The default allowlist is only `camera-01`.
Choose Local or Forwarded, then start. Both players read **the same route and
source**. One leaves browser controls untouched; the other requests zero on
every received audio/video track. A fresh connection is used for every run.

Keep the page visible. After both video receivers and displayed-frame callbacks
advance, the experiment settles for 10 seconds of continuous playback and collects
about 30 seconds of measurements. A pause during settling restarts that settling
period. It closes both
readers automatically. A 90-second overall limit also covers connection failure.
Stop, hiding the tab, and navigating away close the diagnostic readers. Stop the
Go helper with Ctrl-C when finished.

The large clock is available for a separate filmed-clock observation. Moving
from the old clock page requires pointing the camera at this page's clock.
The automatic report does not read or subtract the filmed digits.

Repeat with **Put the zero target on the left** checked. Compare buffer means,
dropped frames, freezes, callback gaps and visible picture quality. Two extra
readers add load; do not mix runs with other changes to the camera, receiver or
forwarder. A short improvement does not establish reliability on a poor network.

## What is measured

The page uses the real `RTCTrackEvent.receiver` from the pinned MediaMTX 1.21.0
reader, calls `receiver.getStats()`, and detects properties in the page itself.
It does not depend on a browser automation tool exposing native browser methods.

- Standard `jitterBufferTarget` is preferred when present. It uses milliseconds.
- Older `playoutDelayHint` is used only when the standard property is absent. It
  uses seconds. Zero is the only value this diagnostic writes.
- Unsupported properties are never assigned, avoiding a meaningless JavaScript
  property that looks like a supported control. Errors and readback mismatches
  are explicit. A returned zero only confirms the request, not the actual delay.
- The mean compressed-video buffer time is
  `1000 * sum(delta jitterBufferDelay) / sum(delta jitterBufferEmittedCount)`.
  Target and minimum delays use the same denominator. These are interval-based,
  weighted results, not averages of rounded one-second means or lifetime values.
- Decode time uses `totalDecodeTime / framesDecoded` counter changes. Optional
  dropped-frame, freeze and recovery-request counters stay null when unavailable.
- Real `requestVideoFrameCallback` support and optional metadata are reported.
  `expectedDisplayTime - receiveTime`, when available and consistent, is the
  browser's last-packet-to-expected-display estimate. Callback gaps can include
  main-thread scheduling; they do not identify intact original pictures.

Receiver changes, counter resets, long sampling pauses, connection errors,
hidden tabs, too few samples and unsupported zero requests make the comparison
inconclusive. Packet-loss and freeze results do not prove full picture fidelity.
There is no source-frame hash reference in this diagnostic.

These statistics do **not** measure camera exposure, encoding, the incoming SRT
buffer or total camera-to-screen delay. `captureTime`, when available, is only
reported as a supported field: this pipeline can rebuild timestamps, so it is
not treated as a verified original-camera clock. Do not add potentially
overlapping buffer, decode and frame-callback figures into a claimed total.

The live `#left-diagnostics` and `#right-diagnostics` elements contain whitelisted
counter totals and playback state as JSON text: statistics timestamps, byte and
frame totals, currentTime, paused/readyState, dimensions, track/transport state,
callback count and callback age. Visibility and focus are recorded separately:
an embedded browser can report visible even while its host panel is not active.
No-frame intervals are marked unusable immediately. These diagnostics distinguish
fresh statistics with frozen video from stale statistics or paused playback;
they do not infer which upstream component caused the stall. Up to 100 diagnostic
samples per reader are retained in the final report.

The on-page `#report` element contains the complete JSON report as text. A
browser automation tool can read that DOM text without reaching into page
globals. **Save measurement JSON to project** sends that report to the helper's
same-origin `/report` endpoint. The helper validates the schema and 1 MiB size
limit, then writes a server-named `reports/browser-buffer-*.json` file with mode
0600 under `--root` (default: the current directory). The page displays the saved
absolute path. No browser Downloads integration is required. The endpoint rejects
foreign or missing Origins, non-JSON content and client-supplied destinations.
Report files are not served back over HTTP. The report excludes
source URLs/IDs, credentials, SDP, ICE addresses and full raw statistics.

## Local boundary

The helper binds to `127.0.0.1`, validates the Host and browser Origin, and serves
only embedded assets plus restricted WHEP routes. Its proxy can reach only ports
18889 and 28889 on loopback, only explicitly allowed source IDs, and only WHEP
OPTIONS/POST or UUID session PATCH/DELETE. It rewrites session Location headers.
It never forwards browser Authorization, Cookie or Origin headers. Requests and
responses are bounded to 256 KiB, with timeouts. No MediaMTX CORS changes are
needed. This remains a local diagnostic, not a remote authenticated video portal.

The shared `internal/viewer/assets/reader.js` is unchanged MediaMTX 1.21.0 code.
Its MIT license is included as `internal/viewer/assets/mediamtx-LICENSE.txt`.
Both assets remain available from the same browser URLs.

## Checks and primary references

```sh
go test ./cmd/browser_check
node --test cmd/browser_check/metrics_test.mjs
node --check cmd/browser_check/assets/app.mjs
```

These tests check calculations, unavailable controls, error handling and proxy
boundaries. They do not substitute for a real browser run.

- [W3C receiver buffer target](https://www.w3.org/TR/webrtc/#dom-rtcrtpreceiver-jitterbuffertarget)
- [W3C buffer statistics](https://www.w3.org/TR/webrtc-stats/#dom-rtcinboundrtpstreamstats-jitterbufferdelay)
- [Chromium receiver controls](https://chromium.googlesource.com/chromium/src/third_party/+/refs/heads/main/blink/renderer/modules/peerconnection/rtc_rtp_receiver.idl)
- [Video frame callback specification](https://wicg.github.io/video-rvfc/)
