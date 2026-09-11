# Browser buffering and a shorter camera wait

The working camera connection used a 120 ms recovery allowance, with direct local forwarding and no extra video compression. A fresh stock-player screenshot read 48.80 seconds now and 48.42 seconds in each picture: about 0.38 seconds of total delay.

## Browser test

The separate comparison page at `http://127.0.0.1:19081/` opens two readers of the same selected source. One uses the browser default; the other asks for `jitterBufferTarget = 0`. This setting is a request to reduce the time the browser holds video before decoding or display. It is not a promise of zero delay.

Unlike the earlier read-only DOM inspection, JavaScript inside the page confirmed support for both this control and `requestVideoFrameCallback`. The initial support conclusion was therefore corrected. The diagnostic uses the pinned MediaMTX reader and a same-origin proxy restricted to named sources on the existing loopback receivers. It does not open the viewers or management API to the network.

After 10 seconds of continuous playback, the page measures about 30 seconds. It subtracts successive cumulative counters and weights buffer time by the number of emitted frames. Stopped frames, missing counters, receiver changes or an interrupted measurement make the comparison unusable. An accepted zero value is reported separately from the actual measured wait.

Two usable observations gave:

| Observation | Default buffer | Zero-request buffer | Frames per viewer | Reported drops / freezes |
| --- | ---: | ---: | ---: | ---: |
| First observation | 59.30 ms | 59.53 ms | 900 | 0 / 0 |
| Completed repeat | 72.01 ms | 71.34 ms | 901 | 0 / 0 |

The second run measured approximately 97 ms from the last packet's arrival to expected display, including about 1.6–1.8 ms of decoding. This interval overlaps buffer time; the two must not be added. Capture timestamps were unavailable. Neither interval measures the entire journey from the camera sensor to the screen. The results show no useful improvement from the zero request, so normal browser buffering remains the selected behavior. [Measured aggregates](../reports/browser-buffer-initial-comparisons.json), [diagnostic instructions](../cmd/browsercheck/README.md), [browser buffer definition](https://www.w3.org/TR/webrtc-stats/#dom-rtcinboundrtpstreamstats-jitterbufferdelay).

## Why lowering only the camera setting would not work

The pinned receiver's SRT library normally requires at least 120 ms. It agrees on the larger of its own minimum and the sender's request. A camera asking for 80 ms would still receive a 120 ms allowance.

A separately built experimental receiver lowers only that minimum to 60 ms. A sender asking for 80, 120 or 300 ms still gets its requested value. Encryption, the earlier clock correction and the reverse-direction 120 ms minimum are retained. The active choice for a camera experiment is 80 ms; a 60 ms minimum does not mean that camera automatically uses 60 ms.

The experimental build passed encrypted negotiation checks for requests of 20, 60, 80, 120 and 300 ms, the receiver's SRT race tests, and the clock-correction regression test. Repeated builds were byte-identical. [Build evidence](../reports/experimental-receiver-lan60.json), [rebuild script](../scripts/build-mediamtx-lan60.sh).

## Generated-video comparison

The existing exact-frame benchmark compared 80 and 120 ms in 16 isolated trials. The simultaneously published clean reference always used 120 ms. Every trial had 270 expected pictures in a fixed nine-second scoring window. Two trials per setting produced the counts below. Every timing comparison passed its validity checks.

| Simulated connection | Correct pictures at 80 ms | Correct pictures at 120 ms |
| --- | ---: | ---: |
| Clean | 540 / 540 | 540 / 540 |
| Each direction delayed 20 ± 10 ms, with 1% packet loss | 540 / 540 | 540 / 540 |
| Each direction delayed 60 ± 20 ms, with 1% packet loss | 294 / 540 | 475 / 540 |
| 200 ms interruption | 540 / 540 | 540 / 540 |

On the clean connection, 80 ms delivered matched pictures about **39–40 ms sooner**. Both clean trials preserved every expected picture. During the interruption, 80 ms retained all tested pictures but produced longer gaps between arrivals. In the harder delay-and-loss condition, both settings damaged or lost pictures, with 80 ms doing substantially worse. These results qualify a local experiment, not a general setting for unreliable networks. Equal random seeds do not produce identical packet-loss histories once recovery traffic differs.

Run the comparison from the project root:

```sh
go run ./cmd/netcheck \
  -mediamtx .tools/mediamtx-v1.21.0-clockfix1-lan60/mediamtx \
  -latencies 80,120 -seeds 17,41
```

[All trials and method](../reports/network-delay-80ms-comparison.json). The normal 300 ms starting setting for unknown senders remains unchanged.

## Active 80 ms experiment

The experimental receiver is active for both services. After the sender restarted, the receiver confirmed **80 ms** on camera-01. Local forwarding still copies the original compressed video. All **85 media acceptance checks passed**, including full decoding of **44 finalized clips**, with no cleanup errors. [Acceptance report](../reports/integration-lan80-receiver.json).

A 50.075-second check observed the same 80 ms connection throughout, with 9,157 additional received packets and no reported dropped or retransmitted packets. This short observation does not establish long-term reliability. [Connection check](../reports/camera80-stability-check.json).

Two further browser comparisons, with left and right positions swapped, found no useful benefit from the zero buffer request:

| 80 ms camera run | Default buffer | Zero-request buffer | Decoded frames, default / zero | Reported drops / freezes |
| --- | ---: | ---: | ---: | ---: |
| Zero on left | 49.83 ms | 49.81 ms | 902 / 902 | 0 / 0 |
| Zero on right | 50.58 ms | 50.86 ms | 900 / 901 | 0 / 0 |

Both measurements passed the page's validity checks. The one-frame count difference is consistent with independent sampling boundaries; it is not evidence of a lost frame. Normal browser buffering remains selected. Full reports and activation evidence are linked from [the activation report](../reports/camera80-activation.json).

## Physical camera reading

The user's screenshot at 02:23:07 on 12 September 2026 shows 34.31 seconds on the large clock, 33.99 locally and 33.95 after forwarding. The picture ages are therefore **0.32 seconds locally** and **0.36 seconds after forwarding**. Compared with the earlier same-page reading of 0.38 seconds in both views, these are improvements of 60 and 20 ms. One pair of screenshots cannot isolate the transport saving from frame timing and independent player buffers. The controlled clean-network experiment separately measured approximately 40 ms of improvement.

This is one approximate reading, not a delay percentile or maximum. An attempted second reading showed a dark scene and was excluded. The 80 ms value is a network recovery allowance, not total camera-to-screen delay. [Physical evidence](../reports/physical-delay-check.json).

For continuous local-versus-forwarded viewing, use `http://127.0.0.1:19080/`. The browser-buffer experiment is separate at `http://127.0.0.1:19081/`; it closes its two diagnostic readers after each run and can save its full measurement JSON privately under `reports/`. To restore the longer camera recovery allowance, import `.local/connections/camera-01-larix-120ms-qr.png` and restart sending, or set the current Larix connection's latency to 120 ms and restart.
