# Further delay reduction

This document records the 120 ms camera and local-forwarding phase. The later [80 ms experiment](browser-and-camera-delay.md) is now active and measured approximately 0.32 seconds locally and 0.36 seconds after forwarding in one physical-clock screenshot. Its separate acceptance run passed 85 checks with 44 fully decoded clips. Historical measurements below are unchanged.

The earlier working settings produced about 0.52 seconds of picture age locally and 0.72 seconds after forwarding. The next changes preserve the 720p picture and avoid another round of compression.

## Camera connection

Camera-01's Larix connection was changed from a 300 ms recovery allowance to 120 ms. The existing app connection needed to be stopped and started before the new allowance took effect. The Mac subsequently confirmed both send and receive allowances of 120 ms on the new connection. One reading after 33,228 received packets showed no receiver-reported drops or received retransmissions; this is a short observation, not a long-term reliability result.

The user's screenshot at 01:24:49 read:

| Clock | Seconds | Picture age |
| --- | ---: | ---: |
| Current | 56.73 | — |
| Local picture | 56.35 | 0.38 seconds |
| Forwarded picture | 56.21 | 0.52 seconds |

Both connections were still using SRT at this point. Exposure, screen refresh and video frame timing limit screenshot precision. The evidence is in [the camera check](../reports/camera-120ms-check.json).

Larix on this device exposes connection details through **Settings → Connections → Manage → connection name**. Tapping the connection in the ordinary list does not open its settings. A private 120 ms import and a separate 300 ms restore import are in `.local/connections/`. Both retain the existing endpoint, credentials and encoder choices. [Official import fields](https://softvelum.com/larix/grove/).

The shorter camera allowance is an experiment for this local network. Earlier controlled loss tests showed that 120 ms can lose or damage pictures where 300 ms preserved them. Restore 300 ms if the picture becomes unstable; a wider field deployment needs its own network measurements.

## Forwarding within one computer

The two video services in this lab run on the same Mac. The SRT recovery allowance adds waiting even on this short connection. The new `local` choice uses authenticated RTSP over TCP between those services. TCP carries the stream through the operating system's local connection; no router or Wi-Fi link is involved. This removes the SRT waiting allowance and the MPEG-TS wrapping step from this part of the path. The video remains compressed in its original form. MediaMTX supports [forwarding through an RTSP connection](https://mediamtx.org/docs/features/forward).

The destination is fixed to `127.0.0.1`. The receiver only listens locally and requires the existing publisher credentials. This RTSP connection is not encrypted, so the local choice must remain restricted to one trusted computer. The camera connection remains encrypted. The general forwarding default and upgrade behavior remain SRT.

Commands while the lab is running:

```sh
./lab --source camera-01 relay-link local
./lab --source camera-01 relay-link srt
```

Each change reconnects only that source's forwarder. Input and recording are separate. `relay-wait 120|300` saves an allowance for SRT; it does not affect an active local connection. `profile copy|small` works with either link.

## Measured transport comparison

The comparison used generated 720p30 video, unique decoded-picture hashes, identical decoders and one monotonic arrival clock. Each trial scored every source picture from seconds 6 through 17: 330 expected pictures. The startup exclusion was fixed in advance to allow normal FFmpeg stream discovery. Each option was tested twice.

| Forwarding option | 95th percentile of added delay in each trial | Correct pictures |
| --- | ---: | ---: |
| SRT with 120 ms allowance | 197–200 ms | 660/660 |
| Authenticated local RTSP/TCP | About 41 ms | 660/660 |

The 95th percentile means 95% of the matched pictures had no more than that added delay. These are short generated-video measurements, not a camera-to-screen guarantee. The benchmark used two paths in one isolated receiver process; the normal lab uses two processes. [Complete report](../reports/relay-transport-check.json).

Extra output flushing and zero muxing-delay options showed no useful gain. One trial with those flags had inconsistent video timestamp mapping and was marked timing-inconclusive; its exact hashes still established intact pictures. Those flags were not selected. The receiver's queue drains as data arrives, so reducing its capacity would not remove a fixed waiting period. Recording input options were left unchanged.

## Recovery check

All **85 media acceptance checks passed**, with no cleanup errors. All **46 completed test recordings decoded fully**. The checks cover authentication, two simultaneous sources, switching between local and SRT forwarding, both picture settings, forwarder crashes, and a frozen receiver. [Acceptance report](../reports/integration-local-forwarding-85-checks.json).

The receiver was deliberately paused for 18.69 seconds. The blocked local forwarder was replaced after 14.19 seconds while both camera inputs and recorders continued. After the receiver resumed, the local forwarder's video decoded within 5.37 seconds; the other source's SRT video decoded within 1.63 seconds. A separate local-forwarder crash recovered within 5.02 seconds. These recovery times include the decoding probe and describe the tested failures, not ordinary picture delay or a general availability guarantee.

An output-side three-second RTSP timeout bounds individual connection and socket waits. FFmpeg 8 ignores some media-write errors, so that setting alone does not guarantee that the forwarding process exits promptly. A separate progress check watches processed video timestamps. After a 12-second startup or reconnect allowance, it restarts only the local forwarder if input remains active but output stops advancing for 12 seconds or falls more than three seconds behind its previous pace. Input gaps and publisher changes reset the evidence. The parser has bounded memory and concurrent access is protected. Advancing processing timestamps do not prove successful delivery to the receiver; the recovery test separately verifies decoded video. [FFmpeg RTSP connection timeout](https://github.com/FFmpeg/FFmpeg/blob/n8.0/libavformat/rtsp.c#L1825), [media-write error handling](https://github.com/FFmpeg/FFmpeg/blob/n8.0/libavformat/rtspenc.c#L164).

Local forwarding is now active for camera-01. The live receiver confirms an RTSP/TCP publisher and no SRT forwarder. The physical camera reconnected with a 120 ms allowance. Recording and R2 uploads resumed. Camera-02 retains SRT and its 300 ms allowance.

## Physical camera result

The user's screenshot at 01:40:51 and a subsequent browser screenshot show the result after local forwarding was enabled:

| Reading | Current clock | Clock in local video | Clock in forwarded video | Local picture age | Forwarded picture age |
| --- | ---: | ---: | ---: | ---: | ---: |
| User screenshot | 46.59 | 46.21 | 46.23 | 0.38 s | 0.36 s |
| Browser screenshot | 77.61 | 77.27 | 77.23 | 0.34 s | 0.38 s |

Before this round of changes, the measured ages were 0.52 seconds locally and 0.72 seconds after forwarding. Reducing the camera allowance brought them to 0.38 and 0.52 seconds. Local forwarding then reduced the forwarded picture age to approximately **0.36–0.38 seconds**, roughly half the starting value. The original 720p compressed video is copied without another encode.

The viewers choose display times independently. The first screenshot's forwarded picture is 20 ms newer than the local picture; that does not imply negative transport delay. Across these two observations the views are almost level, with differences of tens of milliseconds. These readings do not establish a delay percentile or maximum. The user screenshot is preserved privately and all readings are in [the physical delay report](../reports/physical-delay-check.json).

A later live check confirmed 1280 × 720 H.264 on both outputs, with no B-frames. Over a further two-minute observation, the same forwarder and recorder remained running while received bytes and completed recordings increased. The camera receiver reported no drops across 56,938 packets since reconnection. R2 was actively uploading. These are short checks; [the activation report](../reports/local-forwarding-activation.json) preserves their scope and counters.

## Remaining limits

The initial read-only DOM inspection did not expose `requestVideoFrameCallback`. A later diagnostic running JavaScript in the actual page confirmed that this browser supports it, along with `RTCRtpReceiver.jitterBufferTarget`. The earlier inspection therefore did not establish browser support correctly. Two later comparisons accepted a zero buffer target but showed almost identical buffering with the default and zero settings: about 59 ms in one run and 71–72 ms in another. No browser buffer override was adopted. [Measured aggregates](../reports/browser-buffer-initial-comparisons.json).

The camera still has exposure, compression and frame timing costs; changing the controller language does not remove those costs.

This work establishes the fastest successful options in these experiments. It does not establish the absolute minimum possible on this hardware or reliable behavior across untested networks.
