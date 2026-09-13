# Test shorter recovery waits

The question is whether a 120-millisecond waiting allowance delivers useful video sooner than 300 milliseconds. Useful means a correct picture that arrives in time. A low delay among the few surviving pictures is not enough.

## Decision rule

These rules were set before the full comparison:

- On a clean connection, 120 ms must preserve every scored picture, show no changed pictures, leave no more than 150 ms between successive correct pictures, and deliver at least 99% of expected pictures within 250 ms of the clean reference.
- On impaired connections, compare the fraction of all expected pictures that arrive correctly within 250 and 500 ms of the clean reference. Also compare the longest gap between correct pictures and the longest sequence of missing or changed source pictures.
- Keep 300 ms as the starting setting for an unknown network. A successful short clean test qualifies 120 ms for that tested situation only. If both settings lose pictures, neither has established reliable delivery under that condition.

## Results on 12 September 2026

All 16 comparisons passed the experiment's validity checks. Every clean reference had all 270 expected pictures. The 120 ms setting passed both clean trials with every picture intact, every picture within the 250 ms relative deadline, and a maximum gap of approximately 51 ms between correct pictures. The 300 ms setting added approximately 180 ms.

| Condition | Correct pictures at 120 ms | Correct pictures at 300 ms | Observed difference |
| --- | ---: | ---: | --- |
| Clean | 540/540 | 540/540 | 120 ms delivered pictures about 180 ms sooner |
| Short delay and 1% loss | 540/540 | 540/540 | Both kept all pictures; 120 ms delivered sooner |
| Longer delay and 1% loss | 529/540 | 540/540 | 120 ms lost three pictures and changed eight; 300 ms kept all |
| 200 ms interruption | 540/540 | 540/540 | 120 ms had a gap of up to 118 ms between correct pictures; 300 ms up to 43 ms |

Each row combines two nine-second scoring windows. These counts do not predict long-run failure rates. In the longer-delay case, the 300 ms setting delivered all pictures within 500 ms of the clean reference, but almost none within 250 ms. The choice therefore depends on both the permitted picture age and tolerated picture damage.

At this stage, camera-01's forwarding connection between the services on this Mac was set to 120 ms. Its camera-to-Mac connection was still at 300 ms, as were camera-02 and the general default. Live receiver statistics confirmed those two camera-01 waits. The updated controller passed all 56 media checks, including both wait changes, source isolation and full decoding of 28 finished recordings.

The physical camera check then measured approximately **0.52 seconds locally and 0.72 seconds after forwarding**, compared with the earlier 0.52 and 0.92 seconds. Two screenshot readings agreed. This is a measured reduction of about 0.20 seconds for the forwarded picture; it does not establish a maximum delay. See [the filmed-clock readings](delay-investigation.md#controlled-comparison-and-physical-confirmation).

See [all results and relative-delay distributions](../reports/network-delay-comparison.md), [the complete machine-readable report](../reports/network-delay-comparison.json), and [the integration report for this stage](../reports/integration-srt-wait-56-checks.json).

Camera-01 was subsequently changed to a 120 ms camera allowance and local RTSP/TCP forwarding. See [the later optimization](further-delay-optimization.md) for the current configuration and physical measurements.

## Video and measurement

The Go command `cmd/netcheck` generates a 12-second video at 1280 × 720 and 30 pictures per second. It compresses it once as H.264 Baseline at a target 2 Mbps, with no B-frames and an independent keyframe every second. It decodes that encoded file to establish the expected pictures. This avoids treating the normal loss of detail from compression as damage caused by the network.

Every expected decoded picture has a distinct hash: a short fingerprint calculated from all its pixels. The program checks uniqueness before testing. Exact matching pictures are identified by this fingerprint, even if their video timestamps disagree. Hashes here compare generated test images; they do not authenticate a remote camera.

One publisher sends the same encoded video to two separately published paths on an isolated receiver at a fixed rate of one video second per real second. One branch is a clean reference with a fixed 120 ms wait. The other has the requested 120 or 300 ms wait and passes through the network simulator. Separate bounded sender queues prevent one branch from normally blocking the other. Overflow invalidates the comparison. Both branches use encryption and identical single-threaded decoders. They share the receiver process and the Mac's resources; the clean-reference checks help detect interference but do not establish behavior on separate machines.

The Go program timestamps decoded picture arrivals on one monotonic clock: a clock measuring elapsed time without following wall-clock corrections. For each exact matching picture, it subtracts the reference arrival time from the tested branch's arrival time. Negative differences are retained. The result includes receiver, decoder and host scheduling overhead. It is **extra delay against the clean reference**, not absolute camera-to-screen delay.

Only original source positions 60 through 329 are scored: 270 expected pictures over nine seconds. The first two seconds allow startup; the last second is excluded to avoid shutdown effects. Missing, changed or unpaired pictures remain in the denominator of the timely-delivery percentages.

Some decoders output a damaged picture instead of no picture. The report counts those separately where a consistent timestamp mapping can locate them. It also reports outputs whose position cannot be established. Exact image matches remain identifiable without that mapping.

## Controlled network conditions

| Condition | Change applied to both directions |
| --- | --- |
| Clean | No injected delay or loss |
| Short delay and loss | 20 ms one-way delay, with a random variation of ±10 ms; independently discard 1% of packets |
| Longer delay and loss | 60 ms one-way delay, with a random variation of ±20 ms; independently discard 1% of packets |
| Brief interruption | Discard every packet for 200 ms, starting four seconds after the first media packet |

The simulator affects connection messages and recovery requests as well as video packets. Each condition runs with seeds 17 and 41 at both waiting values: 16 trials. A seed reproduces the sequence of random choices, but operating-system scheduling and retransmissions can change which actual packets encounter those choices. These are comparable conditions, not identical packet traces.

A minimum heap holds delayed packets in delivery-time order. This is a data structure that quickly finds the next packet due. The queue has a fixed size limit, and overflow is reported. It makes the simulator controlled; it does not remove the recovery waiting allowance in the video protocol.

## Checks on the experiment itself

Timing is marked inconclusive if the reference fails to preserve all 270 pictures, a timestamp mapping is inconsistent, the reference develops a gap over 200 ms or drifts over 150 ms from the expected frame rate, a sender queue overflows, simulator capacity drops packets, the agreed wait differs from the requested one, or a process fails.

An early pilot exposed FFmpeg's faster-than-real-time catch-up behavior. Explicit one-times input pacing with no initial burst and no catch-up corrected it. The scoring window and 150 ms drift limit were retained. The corrected clean pilot had all 270 pictures and approximately 15 ms of pacing drift.

The report distinguishes gaps between any decoder outputs from gaps between correct pictures. A separate longest-missing-or-changed sequence includes the beginning and end of the scored window; its duration is source-video time, not a measured browser freeze. No page or browser is involved in this test.

## Repeat the experiment

From the project folder, with the corrected receiver selected:

```sh
go run ./cmd/netcheck --root .
```

A clean pilot only:

```sh
go run ./cmd/netcheck --root . --only clean --seeds 17 --latencies 120
```

The tool uses private loopback ports and generated video. It does not load normal camera credentials or contact R2. It writes source video, raw frame arrivals, process logs, parameters, binary/source hashes and per-trial JSON into a new `.local/diagnostics/netcheck-*` directory. Avoid other media tests while it runs: CPU contention changes the measurement.

The code-level tests check scoring of missing pictures, changed pixels and inconsistent timestamps. The separate `./lab test` checks that changing a source's forwarding wait negotiates the requested value, still decodes, and preserves the camera input, both recorders and the other source's workers.

## Limits

These original delay-and-loss trials do not establish long-term availability or behavior on a real radio network. They do not model bandwidth limits, long outages, network changes, power loss, multiple competing devices, camera exposure, browser buffering or thermal limits. A separate, newer [bandwidth comparison](bandwidth-comparison.md) tests saved compression candidates under fixed rate limits; it does not extend the measurements or guarantees of the original trials. The clip here uses a one-second keyframe interval; another encoder or interval can change recovery. Packets still queued at shutdown are not used to score the excluded last second.

After selecting a setting, repeat the physical filmed-clock check and observe picture quality over a longer period. Keep each connection's setting separate so the effect of a change can be measured.
