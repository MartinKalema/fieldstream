# Isolated browser fixture

This serves the **production clock page and monitor code** with conspicuous
TEST ONLY controls. The replacement reader connects two real in-page WebRTC
peers per picture. A 640 × 360 canvas sends 20 manually requested frames per
second. The production monitor receives real track events, receiver statistics
and video presentation callbacks.

No camera, WHEP endpoint, recording, uploader or R2 service is contacted. There
are no external ICE servers. Holding frames stops drawing and `requestFrame()`;
it does not pause the player or close its connection.

From the repository root:

```sh
go run ./cmd/clock_check/testdata/fixture --root . --listen 127.0.0.1:19090
```

The helper snapshots the production HTML, app, watch, optical measurement,
QR worker, bundled QR libraries and styles on startup.
Restart it after changing those files. Open the printed address in a browser
with canvas capture, manual frame requests and WebRTC support.

Test the following in the real page:

1. Both pictures reach **Picture advancing**.
2. **Hold forwarded frames**: the local picture continues, the forwarded warning
   appears, and the fixture status keeps reporting connected peer connections.
3. **Resume forwarded frames**: recovery is checked before the warning clears.
4. Hold and resume both to check each independent warning.
5. Use **Pause forwarded player** and **Play forwarded player** to check the
   production video's separate paused label. These call the actual native
   video's `pause()` and `play()` methods while its source keeps sending.
6. **End forwarded connection**, then use the production **Reconnect this viewer**
   button to create a fresh test source.
7. Hide and restore the page to check observation visibility handling.

For automatic optical-age checks, press the production **Start measurement**
button first. The fixture does not start a measurement automatically. The local
test source copies the current production marker; the following controls change
only the forwarded source:

| Control | Generated picture |
| --- | --- |
| Current marker | Current production marker |
| Add 1 second to marker | A saved copy at least 1 second old; blank during the first second |
| Freeze marker; keep frames moving | The same marker image while the source clock, animation and video frames continue |
| No marker | Advancing video without an optical marker |
| Wrong marker session | A valid QR with a different fixed session |
| Two readable markers | Two copies of the production marker, side by side |

The copied marker is 296 × 296 pixels. Only the forwarded source stores a
history, limited to 30 reusable canvases. Delay selection uses capture time,
so a slow timer cannot turn twenty frames into a falsely labelled one-second
delay. Changing a mode clears that history. Hiding the production target or
closing a source releases the history and frozen copies. No old marker appears
before the production target becomes visible.

The delayed-marker control adds known input delay; the browser's test video
connection and display add further time. Compare relative changes and rejected
markers. Do not expect an exact zero or an exact 1,000 ms end-to-end result. The
frozen-marker test must keep receiving new source frames, so it checks a failure
that a frame-timestamp warning alone cannot identify.

Controls have IDs `fixture-hold-forwarded`, `fixture-resume-forwarded`,
`fixture-hold-both`, `fixture-resume-both`, `fixture-end-forwarded`,
`fixture-pause-forwarded-player`, and `fixture-play-forwarded-player`.
Optical controls have IDs `fixture-age-normal`, `fixture-age-delay`,
`fixture-age-freeze`, `fixture-age-blank`, `fixture-age-wrong-session`, and
`fixture-age-two`.
`fixture-status` shows the actual peer connection states. The test sources close
all peers, tracks and frame timers when the reader closes or the page exits.

This fixture cannot establish camera-to-screen delay or diagnose an upstream
camera stall. It checks the production browser's reaction to controlled frame
interruptions through real browser media APIs.

The directory is under `testdata`, so run its Go tests explicitly:

```sh
go test ./cmd/clock_check/testdata/fixture
go vet ./cmd/clock_check/testdata/fixture
node --check cmd/clock_check/testdata/fixture/reader.js
node --test cmd/clock_check/testdata/fixture/reader_test.mjs
```

The small JavaScript tests check frame-history bounds, delay selection, marker
copying and the independent source controls. Real-browser checks are still
needed for actual QR decoding and the production UI's results.
