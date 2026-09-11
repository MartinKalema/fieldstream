# Camera delay investigation

Measured on 12 September 2026 with a physical camera sending H.264 video to the Mac. Both video services run on the same Mac. These measurements do not represent an internet connection or a difficult radio link.

## What the picture showed

The camera filmed a clock on the Mac. A screenshot contained the current clock and the older clock visible inside each video. Subtracting the numbers gives an approximate picture age.

| Reading | Clock in seconds | Picture age |
| --- | ---: | ---: |
| Current clock | 217.38 | — |
| Local video | 214.46 | 2.92 seconds |
| Forwarded video | 214.10 | 3.28 seconds |

Forwarding added about 0.36 seconds. An independent test matched decoded pictures from both receiver outputs and measured a median extra forwarding delay of 395 ms over 439 matched pictures. This test measured arrival time on the same host clock, not differences between independent media timestamps.

A further probe read a local picture directly, without the browser player. After the probe warmed up, the filmed clock read 525.30 seconds. The current clock at frame arrival was approximately 528.148 seconds, calculated from a browser clock reading and the Mac's wall clock. The picture was therefore already about 2.85 seconds old. Screen refresh, exposure, video frame timing and probe overhead limit the precision.

## Where the waiting occurs

A buffer is a queue of video waiting to be processed or delivered. The receiver's SRT statistics showed:

| Connection | Configured waiting time agreed by the peers | Duration of queued video |
| --- | ---: | ---: |
| Camera to local receiver | 300 ms | About 2,600 ms |
| Forwarder to second receiver | 300 ms | About 300 ms |

The live connection showed little packet loss at the time of this reading. A configured value alone does not establish actual delay; the picture measurement and queue statistics exposed the difference.

## Isolated reproduction

A separate test used the unmodified MediaMTX 1.21.0 binary and an FFmpeg generated video sender on loopback ports. A small UDP proxy blocked the initial connection messages for two seconds in one trial. Once connected, it forwarded traffic normally. It did not touch the physical camera, normal service configuration or R2.

| Trial | Agreed waiting time | Median duration of queued video |
| --- | ---: | ---: |
| Normal connection | 300 ms | 286 ms |
| Initial connection blocked for 2 seconds | 300 ms | 2,346 ms |

The extra delay persisted after connection setup. This independently reproduces the timing failure seen in the camera path.

The receiver must translate the sender's clock into its own clock when the connection starts. The inspected Go SRT library compares packet timestamps against elapsed time since the receiving connection was accepted, without properly accounting for the sender's earlier clock origin. The [reference SRT timing calculation](https://haivision.github.io/srt-rfc/draft-sharabayko-srt.html#section-4.5.1.1) subtracts the timestamp from the connection message when establishing that time reference.

A separate local build, `v1.21.0-clockfix1`, preserves the peer's timestamp before composing the connection response. It establishes a separate receive clock from that timestamp and its local arrival time. The outgoing clock is unchanged. The same isolated reproduction measured 283 ms for the normal connection and 289 ms after the two-second connection delay with this correction. Five focused tests checked encrypted delivery in both directions, older caller and listener clocks, and a counter rollover during connection setup. They passed with Go's race detector, which looks for unsafe concurrent access to shared memory.

All 50 full lab checks passed with the corrected receiver, including two simultaneous sources, access restrictions, forwarding interruptions, worker recovery, picture resizing, full decoding of every orderly-shutdown clip, and persistence after restart. Cleanup reported no errors. The corrected receiver is active. The physical camera reconnected, and recording and R2 uploads resumed. Three further readings measured the local receiver's queue at 265–268 ms and the forwarding receiver at 299–300 ms.

The user's screenshot at 00:41:16 then confirmed the physical camera-to-browser improvement:

| Reading after correction | Clock in seconds | Picture age | Previous picture age |
| --- | ---: | ---: | ---: |
| Current clock | 31.04 | — | — |
| Local video | 30.52 | 0.52 seconds | 2.92 seconds |
| Forwarded video | 30.12 | 0.92 seconds | 3.28 seconds |

The reduction is approximately 2.4 seconds. Forwarding still adds about 0.4 seconds. These are approximate readings from individual screenshots, not delay percentiles or a guaranteed maximum. The corrected screenshot was saved privately as `.local/diagnostics/physical-delay-after.png` and its readings were added to `reports/physical-delay-check.json`.

MediaMTX's SRT tests passed with the race detector. The broader GoSRT root-package suite passed after excluding two tests that also fail on the unchanged pinned dependency on this Mac: an existing race in `TestEncryptionRetransmit` and a timeout in `TestListenMultipleIPs`. Details are in [the patch notes](../patches/README.md). Long-running clock drift correction remains absent in the upstream library; the focused rollover checks do not establish behavior after long idle periods or a real 72-minute session.

## Building and selecting the correction

### Further speed experiment

A separate generated-video experiment requested a 120 ms repair wait on the corrected receiver. It negotiated 120 ms and measured a median queue duration of 99 ms; the corresponding earlier 300 ms experiment measured 283 ms. Queue duration describes the timestamp span of packets currently queued, so it fluctuates with packet timing and is not the same measurement as camera-to-screen delay. The report is `reports/latency-120ms-probe.json`. The physical camera and normal forwarding settings were not changed by this experiment.

Reducing both connection waits from 300 ms to 120 ms could save roughly 360 ms if other delays remain the same. That gives provisional estimates of 0.34 seconds locally and 0.56 seconds after forwarding, based on the last physical-camera screenshot. These estimates require a new camera measurement. Shorter waits leave less time to recover missing packets, so controlled loss and delay tests are needed before choosing a faster default for unreliable connections.

Better data structures and algorithms may help when work accumulates: bounded queues limit how much old work can wait, packet deadlines limit late recovery, and fair scheduling stops one busy source from delaying others. Arbitrarily dropping compressed video frames is unsafe because later frames may depend on them. Processing must be measured before replacing queue structures; a faster lookup does not remove a deliberately configured waiting deadline.

### Controlled comparison and physical confirmation

The [controlled network experiment](network-delay-test.md) then compared 120 and 300 ms over 16 trials. The shorter wait preserved all scored pictures on the clean connection and saved about 180 ms. With a longer network delay and 1% packet loss, 120 ms preserved 529 of 540 scored pictures while 300 ms preserved all 540. The longer wait gave those pictures more time to arrive. These short trials establish a tradeoff, not a general reliability guarantee.

At this stage, camera-01's forwarding connection used 120 ms between the services on this Mac. Its camera-to-Mac connection was still at 300 ms. Camera-02 and the general default remained at 300 ms. The new `relay-wait` command accepts either value per source; it changes only that forwarder. All 56 updated media checks passed, including negotiated waits, source isolation and full decoding of 28 finalized clips. Live receiver statistics subsequently confirmed 300 ms on the physical camera connection and 120 ms on forwarding.

The user's screenshot at 01:08:25 confirmed the resulting picture age:

| Reading with the shorter forwarding wait | Clock in seconds | Picture age | Previous picture age |
| --- | ---: | ---: | ---: |
| Current clock | 27.49 | — | — |
| Local video | 26.97 | 0.52 seconds | 0.52 seconds |
| Forwarded video | 26.77 | 0.72 seconds | 0.92 seconds |

An earlier browser screenshot in the same session read 21.59, 21.07 and 20.87 seconds, giving the same two ages. The observed forwarding improvement is about 0.20 seconds, consistent with the controlled comparison. The local picture age is unchanged because its waiting allowance was unchanged. These remain approximate screenshot readings, not a guaranteed delay. The original user screenshot is saved privately as `.local/diagnostics/physical-delay-relay120.png`; measurements and activation checks are in `reports/physical-delay-check.json` and `reports/relay-wait-activation.json`.

### Shorter camera allowance and local forwarding

The camera's Larix connection was then changed to 120 ms and restarted. The receiver confirmed that setting. The user's 01:24:49 screenshot read 56.73 seconds now, 56.35 in the local picture and 56.21 after forwarding: ages of **0.38 and 0.52 seconds**. Forwarding still used SRT with a 120 ms allowance in that screenshot.

Because both video services run on one Mac, a selectable authenticated RTSP/TCP connection removes the SRT allowance between them. Two short generated-video comparisons preserved all 660 expected pictures with either option. The local option's added delay at the 95th percentile was about 41 ms, compared with 197–200 ms for SRT at 120 ms. This measures added delay in the test harness, not camera-to-screen delay.

The local option includes a bounded progress watcher to restart a stalled or falling-behind forwarder while input continues. All **85** full media checks passed, including a frozen receiver and complete decoding of **46** finalized test recordings. After activation, the user's 01:40:51 screenshot measured **0.38 seconds locally and 0.36 seconds after forwarding**. A second browser screenshot measured **0.34 and 0.38 seconds**. The two players can differ by a frame or more; these are approximate observations.

At this stage camera-01 used the 120 ms camera allowance, local forwarding and the unchanged-video copy profile. Recording and R2 uploads resumed. Camera-02 and the general forwarding default retain SRT at 300 ms. Local forwarding is restricted to this trusted computer; it is not a choice for an untrusted network. `./lab --source camera-01 relay-link srt` restores the saved SRT forwarding allowance without changing camera input or recording. See [further measurements and recovery behavior](further-delay-optimization.md) and [the 85-check report](../reports/integration-local-forwarding-85-checks.json).

The subsequent [80 ms camera experiment](browser-and-camera-delay.md) uses a separately tested receiver minimum, with local forwarding unchanged. The physical screenshot at 02:23:07 gives approximately 0.32 seconds locally and 0.36 seconds after forwarding. The latest receiver passed all 85 acceptance checks and fully decoded 44 finalized clips. Browser-buffer requests gave no useful improvement and were not adopted as a default.

### Build and rollback

`scripts/build-mediamtx-clockfix.sh` rebuilds the correction from pinned Go modules and the patch in `patches/`. It checks module identities and checksums, runs the focused tests, and creates a separately named binary plus build information. It uses Go 1.26.8 to match the official receiver's toolchain. It does not select or restart a receiver. The lab controller itself still builds with Go 1.25 or newer.

The default output is `.tools/mediamtx-v1.21.0-clockfix1`. If that directory exists, the script refuses to overwrite it; a different output directory can be passed as its first argument.

The controller uses `.tools/mediamtx-active/mediamtx` when that selection exists. In this workspace, the directory link points to `mediamtx-v1.21.0-clockfix1`. The original official binary remains in `.tools/mediamtx-v1.21.0/mediamtx`. Setup manages the official download separately. A broken explicit selection fails at startup; it does not silently restore the old receiver.

To restore the official receiver, stop the lab while the camera is still sending, remove **only the `.tools/mediamtx-active` link**, and start the lab again. This retains the source settings, recordings and R2 queue. Restoring the old receiver also restores its observed timing defect. A receiver change interrupts the live connection; the sender may need to reconnect.

The local evidence is in `reports/physical-delay-check.json`, `reports/relay-delay-before.json`, `.local/diagnostics/srt-clock-repro-results.json` and `.local/diagnostics/srt-clock-repro-fixed-results.json`. The diagnostics folder also contains the reproduction harness and the private camera-frame capture. It is excluded from version control.
