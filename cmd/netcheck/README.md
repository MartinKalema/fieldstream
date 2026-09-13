# Controlled video network comparison

For the separate rate-limit experiment, run `go run ./cmd/netcheck --bandwidth-compare`
and follow the [bandwidth comparison guide](../../docs/bandwidth-comparison.md).
It compares three saved encodings at unlimited, 1,500 and 900 kbps with a fixed
120 ms recovery allowance. The method below describes the original delay, loss
and interruption experiment, which remains the default command.

Run `go run ./cmd/netcheck` from the project directory. The command uses the
selected corrected MediaMTX binary, creates private test credentials and fresh
loopback ports, and saves evidence under `.local/diagnostics/netcheck-<time>/`.
It does not read the lab's normal settings, use a physical camera, change the
active services, or send anything to R2. Avoid other heavy tests during a run.

The complete comparison takes about four minutes. A short check is available:

```sh
go run ./cmd/netcheck -only clean -seeds 17 -latencies 120
```

Use `-ffmpeg /path/to/ffmpeg` when FFmpeg is elsewhere, and `-mediamtx /path/to/mediamtx`
to choose an explicitly corrected receiver. The code checks the receiver's
version before starting. It runs on macOS and Linux with FFmpeg, including
libx264 and SRT support. Stop it with Ctrl-C; its child processes are cancelled.

The default comparison remains 120 and 300 ms. Experimental `-latencies 80,120`
or `60,120` require a receiver with a sufficiently low minimum. A mismatch in
the actual negotiated allowance makes the timing result inconclusive. See
[the later 80 ms experiment](../../docs/browser-and-camera-delay.md).

## What it measures

The command encodes one twelve-second, 1280×720, 30-frame-per-second test video
with H264 Baseline and one keyframe each second. It decodes that encoded file
once to record the exact expected pixel hash of every frame. Repeated hashes
cause the experiment to stop because they would make frame identity ambiguous.

For each trial, one publisher reads that file at exactly its original rate and
copies the encoded frames into two independent, bounded output queues. Both
connections use AES encryption. One goes directly to the isolated receiver with
a fixed 120 ms repair buffer. The other goes through the controlled network
proxy, requesting either 120 or 300 ms. Actual negotiated settings are read back.

Two identical FFmpeg decoders emit pixel hashes. One Go process stamps both
outputs with the same monotonic clock. The difference in arrival time of the
same exact frame is the reported added delay. This includes receiver and decoder
work and operating-system scheduling. It is **relative to the simultaneous
120 ms reference**, not camera-to-screen delay or pure network delay. Negative
differences are preserved. Initial encoding and shared publisher delay are not
measured by this subtraction.

Frames 60 through 329 form the fixed nine-second scoring window: 270 expected
frames. Every expected position stays in the quality and deadline denominators.
Missing or changed frames therefore cannot disappear from a favorable latency
summary. Exact identities come directly from unique pixel hashes. Timestamps
are used only to locate nonmatching outputs; inconsistent timestamp mapping
makes that classification and the timing comparison inconclusive.

`arrival_gaps_ms` measures gaps between all decoded outputs. A decoder can fill
a damaged interval with estimated pixels, so this is not intact-picture
continuity. `intact_arrival_gaps_ms` measures gaps between exact expected frames.
The longest run of missing or changed source frames also includes either end of
the scoring window; its duration is source-video time, not a measured wall-clock
pause. Deadline fractions report exact frames arriving within 250 or 500 ms of
the clean reference, divided by all 270 expected frames.

## Network conditions

Each condition runs at both buffer settings with seeds 17 and 41:

| Condition | Each direction |
|---|---|
| Clean | No added delay or loss |
| Short delay and loss | 20 ms plus uniform ±10 ms jitter; independent 1% datagram loss |
| Longer delay and loss | 60 ms plus uniform ±20 ms jitter; independent 1% datagram loss |
| Short outage | Drop both directions for 200 ms, starting four seconds after the first media packet |

Impairment affects control packets, connection setup and retransmissions as well
as original media. The random-number sequence is seeded; actual packet ordering
and scheduling by the operating system are not deterministic. The proxy has
bounded queues and reports any resource-related drops separately from deliberate
faults. Datagrams still queued when the trial is shut down are discarded; the
proxy counters are not a complete packet-delivery accounting ledger.

## Reading the evidence

`results.json` contains every trial, including failures. Each trial directory
contains its summary, frame-arrival evidence and redacted process logs.
`parameters.json` records the method, limits, and source/receiver/harness hashes.
The original encoded clip and its offline frame hashes are retained. The private
receiver configuration contains generated test credentials; share the reports
and redacted logs rather than that configuration.

Timing comparisons are flagged when the clean reference loses expected frames,
its pacing drifts more than 150 ms, it pauses more than 200 ms, timestamp mapping
is inconsistent, an output FIFO overflows, a proxy resource limit drops packets,
or the negotiated delay differs from the request. These checks protect against
mistaking a stalled reference for a faster impaired path. The delay distribution
alone is never evidence that the stream remained intact.

These short synthetic trials qualify only the tested conditions. They do not
establish long-run reliability, radio-link behavior, or physical camera freshness.
