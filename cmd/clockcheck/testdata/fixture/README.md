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
go run ./cmd/clockcheck/testdata/fixture --root . --listen 127.0.0.1:19090
```

The helper snapshots the production HTML, app, watch and styles on startup.
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

Controls have IDs `fixture-hold-forwarded`, `fixture-resume-forwarded`,
`fixture-hold-both`, `fixture-resume-both`, `fixture-end-forwarded`,
`fixture-pause-forwarded-player`, and `fixture-play-forwarded-player`.
`fixture-status` shows the actual peer connection states. The test sources close
all peers, tracks and frame timers when the reader closes or the page exits.

This fixture cannot establish camera-to-screen delay or diagnose an upstream
camera stall. It checks the production browser's reaction to controlled frame
interruptions through real browser media APIs.

The directory is under `testdata`, so run its Go tests explicitly:

```sh
go test ./cmd/clockcheck/testdata/fixture
go vet ./cmd/clockcheck/testdata/fixture
node --check cmd/clockcheck/testdata/fixture/reader.js
```
