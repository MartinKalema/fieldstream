# Local SRT receive-clock correction

`gosrt-a77b40bb4b76-receive-clock.patch` corrects the mapping between a sender's
packet timestamps and the receiver's clock. It applies only to the pinned GoSRT
commit used by MediaMTX v1.21.0. This is a local correction, not an upstream release.

The original receiver started its elapsed-time counter when the connection was
accepted, but compared it directly with timestamps from the sender's older clock.
Time spent establishing the connection therefore became a persistent extra video
delay. Both handshake handlers also replaced the incoming timestamp before using
it. The correction saves the peer timestamp and local receipt time first, creates
a separate receive clock from that pair, and leaves the local send clock alone.
It retains encryption and the negotiated packet repair delay.

The receive clock follows the [SRT reference implementation's mapping](https://github.com/Haivision/srt/blob/v1.5.4/srtcore/tsbpd_time.cpp#L150).
This patch does not add continuous clock-drift correction or change congestion
control, bitrate, recording, or browser playback.

From a clean project checkout, run:

```sh
./scripts/build-mediamtx-clockfix.sh
```

The script downloads Go 1.26.8 through Go's toolchain mechanism when needed,
checks the exact module hashes and commits, applies the patch, and runs the
focused regression tests with the race detector. It uses the upstream generator
to download and verify the hls.js asset, then builds
`.tools/mediamtx-v1.21.0-clockfix1/mediamtx`. Go, a C compiler for race tests, and
the standard `patch` utility are required. A network connection is needed on the
first build. The script creates a build manifest and preserves source and test
logs under `.tools/source/`. It neither activates the binary nor replaces an
existing output directory. An optional first argument chooses a different output
directory for a rebuild.

The test fixture uses the `.go.txt` extension so the controller's own `go test
./...` does not treat it as another Go package. The build script copies it into
GoSRT before testing. It checks encrypted traffic in both directions with normal
clocks, clocks started two seconds earlier, and a 32-bit timestamp rollover. It
also checks that packets retain their repair delay and local send clocks stay
unchanged.

Validation on the development Mac:

- The five focused encrypted tests passed with `-race`.
- MediaMTX's SRT server package tests passed with `-race`.
- An independent FFmpeg/UDP-proxy test negotiated 300 ms throughout. Blocking
  just the initial handshake for two seconds changed the original receiver's
  steady queue from 286 ms to 2,346 ms. With this correction, the same comparison
  was 283 ms versus 289 ms.
- The full upstream GoSRT root-package race suite is not clean on this Mac:
  `TestEncryptionRetransmit` races while its test code changes a callback, and
  `TestListenMultipleIPs` times out. Both failures were reproduced in the
  unmodified pinned dependency. They were not changed by this patch. The remaining
  root-package tests passed with `-race` when those two baseline failures were
  excluded.

These tests establish the connection-clock correction. They do not establish
production readiness or a maximum camera-to-screen delay on an unreliable link.
