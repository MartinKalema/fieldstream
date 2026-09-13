# Compare video delivery when the connection is too small

This experiment asks whether sending a smaller video preserves more correct,
timely pictures when a connection cannot carry the original rate. It sends saved
generated video in real time through an isolated connection limit. It does not
change the camera, live forwarding profile, recorder or R2 uploader.

[Results from the first measured comparison](bandwidth-results.md) are recorded
separately from this method and its acceptance rule.

## Run and repeat

From the project folder, with Go, FFmpeg with libx264/SRT support, and the corrected
MediaMTX receiver selected:

```sh
go run ./cmd/netcheck --root . --bandwidth-compare
```

The defaults compare all three encodings at unlimited, 1,500 and 900 kilobits per
second, twice: 18 trials. To repeat those exact settings explicitly:

```sh
go run ./cmd/netcheck --root . --bandwidth-compare \
  --rates 0,1500,900 --queue-bytes 32768 \
  --encodings copy,detail20,small20 --seeds 17,41
```

For a shorter check of one candidate with and without the limit:

```sh
go run ./cmd/netcheck --root . --bandwidth-compare \
  --rates 0,900 --encodings detail20 --seeds 17
```

Here `--seeds` supplies **repeat labels**. This mode injects no random loss,
variation in travel time, or outage. Reusing a label does not reproduce the
computer's packet scheduling. Encoding order rotates between repeats to reduce
the effect of always testing one candidate first.

Use `--ffmpeg /path/to/ffmpeg` or `--mediamtx /path/to/mediamtx` if needed. This
mode fixes both recovery allowances at 120 ms; it cannot be combined with
`--latencies`, `--only`, or `--relay-compare`. Ctrl-C cancels its child processes.

## What travels through the connection

The tool generates one twelve-second source. Both smaller candidates are made
from that same saved source before transmission.

| Candidate | Picture size | Pictures per second | Target video rate |
| --- | --- | ---: | ---: |
| `copy` | 1280 × 720 | 30 | 2,000 kbps |
| `detail20` | 1280 × 720 | 20 | 1,200 kbps |
| `small20` | 640 × 360 | 20 | 650 kbps |

Each uses H.264 Baseline, no B-frames, and a keyframe every second. These are
encoding targets, not guaranteed network rates. Packet packaging, recovery
requests and retransmissions also consume connection capacity.

For each trial, the **same already-compressed candidate** is split into two
independent, bounded sender queues. One branch is a clean reference; the other
passes through the connection limiter. Both SRT connections must actually agree
to 120 ms, the time allowed for repairing missing data. Both use encryption and
identical decoders. The publisher runs at one second of video per real second,
without an initial or catch-up burst.

Each candidate is first decoded offline to establish its own expected picture
fingerprints. Normal detail lost during compression is therefore separate from
missing or changed pictures caused during delivery. This is not a comparison
against an uncompressed camera scene.

## The connection limit

`0` means no injected rate limit. A positive value limits each direction
independently. Each direction has a first-in, first-out queue holding at most
**32,768 bytes** by default, including the packet currently being transmitted.
Each packet waits for its bytes' share of transmission time. A full queue drops
the new packet and reports an intentional congestion drop.

Unused capacity is not saved up. If the computer wakes the scheduler late, the
next packet starts from the actual send time; it cannot release a catch-up burst.
The configured rate is a conservative nominal ceiling, not a promise that the
computer will achieve exactly that throughput. The evidence includes scheduler
lateness, queue waiting time, forwarded bytes and congestion drops separately
for each direction.

The rate counts complete UDP payloads: video plus SRT headers, setup messages,
control messages and retransmissions. It excludes UDP, IP and physical-network
headers. A measured 900 kbps here is not a claim about a 900 kbps radio link.

## Decide before reading results

A candidate is useful **under one tested condition** only when both repeats pass
the measurement checks and each repeat has:

- Every scored picture intact, with no duplicate outputs.
- At least 99% of expected pictures arriving within 250 ms of the simultaneous
  clean reference.
- No gap greater than 150 ms between successive correct decoded pictures.

Both candidates and `copy` use the same source interval, from 2 seconds inclusive
to 11 seconds exclusive. That means **270 expected pictures** for `copy`
(indices 60–329) and **180** for each 20 fps candidate (indices 40–219). Missing,
changed and unpaired pictures remain in the deadline denominators. Compare
fractions across candidates; their frame totals differ.

Added delay is the arrival time of one correct decoded picture minus the arrival
time of that same picture on the reference branch, using one monotonic clock.
Negative differences are retained. Receiver, decoder and operating-system work
are included. A low delay among a few surviving pictures does not pass the rule.

## Check the measurement first

Before sending video, the tool calibrates every positive rate limit with
1,316-byte packets and a sender targeting twice the nominal rate. After a
500 ms warmup, it measures received bytes over a fixed two-second window; the
calibration has a five-second deadline. It passes only when measured throughput
is 85–101% of the nominal rate, at least 20 packets are scored, no simulator
resource drops occur, scheduler lateness stays at or below 25 ms, and the full
queue deliberately drops packets to show that the limiter was saturated.
Failure saves private evidence and stops the media comparison. Read both nominal
and calibrated rates: this tolerance does not establish exact physical bandwidth.

Read `timing_comparison_conclusive`, `error`, and `flags` before interpreting
each result. A trial is inconclusive if the reference loses or changes expected
pictures, its gaps exceed 200 ms, its pacing drifts more than 150 ms, picture
timestamps cannot be mapped consistently, a sender queue overflows, a simulator
resource limit drops data, a limited-direction scheduler is more than 25 ms late,
the agreed SRT allowance differs from 120 ms, or the trial reports an error.

Intentional drops from a full bandwidth queue describe the tested congestion;
they do not by themselves invalidate the experiment. They can still make the
candidate fail the useful-video rule. A successful command means measurement
checks passed, not that every candidate delivered useful video.

## Resources, evidence and limits

The run requires at least 2 GiB of free disk space, permits at most 36 trials,
and has an 18-minute deadline. Each preparation has a 45-second deadline and rejects a completed
file larger than 32 MiB; each trial has a 25-second deadline. A process lock prevents
two bandwidth comparisons from running together. There are additional limits of
4,096 queued datagrams and 512 pending input datagrams; hitting a simulator
resource limit is reported separately from the intended byte-queue limit.

Custom rates permit up to six distinct values: `0` or 64–100,000 kbps. Queue
capacity permits 2,048–1,048,576 bytes per direction. Up to three distinct repeat
labels are accepted. Avoid other heavy media tests: the isolated services still
share this computer's processor and disk with the live lab. A permitted queue
size or rate is a test setting, not a guarantee that the candidate will pass.

Evidence is saved privately under `.local/diagnostics/bandwidth-*`: generated
clips, offline fingerprints, raw decoded arrivals, per-trial summaries, redacted
logs, a readable `results.md` after the trials finish, and `results.json`. The report identifies the settings, source code and
executables. Private receiver configuration contains temporary test credentials;
keep the whole generated directory out of commits. The tool uses loopback ports
and does not load camera credentials, the recording catalog or R2 settings.

These trials do **not** measure live encoding cost, browser freezes, absolute
camera-to-screen delay, changing bandwidth, competing traffic or long-term
reliability. Preparation CPU time is the cost of making saved files before the
trial. Correct-picture gaps and runs of missing source frames are not browser
freeze measurements. Compression can also remove readable detail and motion
even when delivery is perfect; use the [picture comparison](compression-comparison.md)
and later live measurements alongside this test. No measured outcome is claimed
by this guide.

The [DDIA connection](constraints-and-language-choice.md#applying-designing-data-intensive-applications)
is how waiting work grows when input keeps exceeding processing capacity. A
bigger queue can postpone a drop while showing older pictures. A bounded queue
keeps that waiting finite; a lower video rate may prevent it from filling. Stored
history can wait for an upload, while live viewing needs a separate decision
about how old a picture may become. This experiment tests that design choice.
