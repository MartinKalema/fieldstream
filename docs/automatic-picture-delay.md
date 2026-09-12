# Historical automatic picture-delay experiment

The browser's pattern reader and automatic measurement have been removed with
the live browser players. **GStreamer does not currently measure picture delay
automatically.** The standalone clock page displays only a white elapsed clock.
Use the [manual native-viewer method](gstreamer-viewer.md#make-a-fair-comparison)
for current checks.

The full [method, implementation and tests at commit 40dfbc8](https://github.com/MartinKalema/fieldstream/blob/40dfbc8/docs/automatic-picture-delay.md)
remain historical evidence. Their browser commands do not apply to this checkout.

## What the experiment measured

The page drew changing QR patterns with a test-session identifier and sequence
number. It recorded each pattern's actual draw interval and read that pattern
from its own browser video players. It never read the separate GStreamer window.

If a pattern was shown from 10.00 to 10.10 seconds and sampled from received
video at 10.45 seconds, its approximate age interval was **0.35–0.45 seconds**.
Software draw time is not the exact moment light leaves a display. Screen
refresh, exposure, rolling shutter, scaling and presentation timing added
uncertainty outside that interval. It was not verified camera capture time or a
guaranteed camera-to-screen bound.

One worker sampled at most twice per second per player, with one image in work,
no queued images, a 960 × 540 size limit and a 250 ms acceptance deadline.
Unavailable, unreadable, ambiguous, timed-out and canceled work was counted
separately. Failed readings did not become zero delay. Old current readings
expired; only successful readings contributed to historical summaries. These
limits could miss short events and did not remove the measurement's CPU cost.

## Generated browser checks on 12 September 2026

| Controlled case | Observed result |
| --- | --- |
| Baseline generated marker | Approximate readings around 0–0.13 seconds |
| Marker intentionally delayed by one second | Approximate readings around 0.97–1.07 seconds |
| Marker held while video timestamps advanced | Marker age grew beyond 20 seconds while the progress check still reported advancing video |
| Unreadable marker or another session's marker | Current measurement cleared instead of retaining an old success |

A later baseline repeat accepted 47 of 47 started reads for each picture, with
historical upper endpoints reaching about 0.16 seconds. A two-marker fixture
produced no new accepted forwarded readings and was reported unreadable.
Separate decoder tests exercised explicit ambiguity with two readable codes.
Lifecycle tests covered late replies, interrupted observations, reconnects and
canceled work. These were browser checks, not native-display validation.

## Physical camera: 12 September 2026

A 120-second run used Larix on the iPad, the copy profile and local forwarding
between programs on the same Mac. The camera closely framed the direct white
target. The exact in-app browser version was not captured.

| Completed run | Local picture | Forwarded picture |
| --- | --- | --- |
| Successful / started reads | 192 / 232 | 200 / 231 |
| Median accepted midpoint | 0.340 s | 0.370 s |
| Largest sampled upper endpoint | 0.480 s | 0.500 s |
| Late presentation checks / worker timeouts | 0 / 0 | 0 / 0 |
| Checks skipped while the reader was busy | 0 | 12 |

All started reads completed. Failed reads remained missing measurements. The
Larix watermark crossed the pattern, but the observations did not establish
the cause of each failed read. The largest successful endpoint was not a
maximum for the system; failed or unsampled pictures were excluded.

Nearby manual screenshots provided a separate comparison:

| Direct clock | Manual local / forwarded | Nearby automatic local | Nearby automatic forwarded |
| --- | --- | --- | --- |
| 409.67 s | 0.34 / 0.38 s | 0.30–0.42 s | 0.32–0.44 s |
| 443.53 s | 0.38 / 0.42 s | 0.22–0.34 s | 0.30–0.42 s |
| 545.46 s, separate repeat | 0.36 / 0.40 s | 0.31–0.41 s | 0.32–0.43 s |

These methods observed different frames at nearby times. In the second row the
manual local reading exceeds the nearby automatic upper endpoint. No fixed
accuracy tolerance or endpoint correction was established.

An earlier duplicate-page run was excluded because the camera filmed a
different page from the one sampled. A whole-monitor view with a small target
and recursive copies produced no accepted automatic readings. Success with a
close target did not establish success at other scales or lighting conditions.

## Requirements before adding native automatic measurement

A replacement must observe the native picture at a clearly stated point in its
display path, use a known clock relationship, and account for display and camera
timing uncertainty. A decoder timestamp alone is not camera capture time.

Keep work bounded, reject stale or wrong-session results, expire old readings,
and report failed and skipped samples. Test known delays, held pictures with
advancing timestamps, unreadable targets, reconnects and suspension. Compare
with repeated manual readings and measure the observer's own cost. The old
browser results do not establish accuracy or reliability for that future tool.
