# Live compression trial on 12 September 2026

The new `detail` profile reduced forwarded payload by roughly 37–40% in this
trial, keeping 1280 × 720 pixels and sending 20 frames per second. Its forwarding
process used about 18–19% of one CPU core. The original `copy` setting used less
than 1% of one core and retained the camera's roughly 30 fps.

The compressed picture was usually a little older. This is a bandwidth-saving
option, not a demonstrated speed improvement on the current local connection.
The lab was returned to `copy`; `detail` remains available for a constrained
connection trial. It does not reduce camera-to-Mac traffic or original recordings
and their R2 uploads.

## Four live observations

One physical camera filmed the clock page and surrounding application window.
The sequence was copy, detail, detail, copy. Both profiles used the same local
forwarding connection. The camera settings, receiver and archive configuration
were kept; the scene had some camera movement and changing screen content, so
these are not identical-input experiments.

Each browser run waited for ten seconds of continuous playback, then collected
about thirty seconds of measurements. It opened two readers: browser default
and a request for the smallest buffer. Their positions were swapped for the
second pair. Each CPU observation then ran for thirty seconds after those two
diagnostic readers closed. Existing viewers, recording and uploads remained
active. No builds or media acceptance tests ran during these four observations.

| Profile and repeat | Forwarded payload | Forwarder CPU, one core = 100% | Average sampled memory | Browser decoded fps | Reported freezes per viewer |
| --- | --- | --- | --- | --- | --- |
| Copy 1 | 2,000 kbps | 0.7% | 29.7 MiB | 29.93 | 4 |
| Detail 1 | 1,195 kbps | 17.9% | 61.5 MiB | 19.99 | 0 |
| Detail 2 | 1,203 kbps | 18.7% | 61.6 MiB | 20.04 | 0 |
| Copy 2 | 1,901 kbps | 0.7% | 29.1 MiB | 29.98 | 1 |

All four CPU windows were complete without missing samples, counter resets or
process identity changes. All four browser comparisons had thirty valid
intervals and no invalid intervals. Both browser modes showed the same freeze
count in each run, 1280 × 720 dimensions, and zero reported dropped frames.
Their decoded rates differed only below the displayed precision.

The first copy run's freezes totalled about 0.85 seconds per viewer, and the
second about 0.21 seconds. The browser's default compressed-video buffer
averaged about 20–21 ms for copy and 45–51 ms for detail. Requesting a zero target
did not remove that buffer. Even the detail runs had frame-callback gaps up to
about 181 ms: zero reported freezes does not mean perfectly even display.

These counters describe the browser's observations. They do not establish why
a freeze occurred, count intact original camera pictures, or prove that
compression prevents stalls. The CPU numbers cover only the forwarding process;
payload rates exclude some network overhead. The camera's encoder, receiver,
browser, recorder and uploader have separate costs.

## Picture age and the late stall

Manual readings from the clock screenshots gave:

| Profile | Ordinary forwarded-picture age readings | Additional observation |
| --- | --- | --- |
| Copy | 0.34, 0.34, 0.36, 0.36 and 0.38 seconds | A post-run reading reached 6.36 seconds |
| Detail | 0.46, 0.46, 0.42 and 0.44 seconds | No comparable long stall was captured in these few readings |

In the four detail screenshots, the forwarded picture was 0–100 ms behind the
local picture. Local and forwarded viewers buffer and select frames independently;
a slightly newer forwarded frame in a copy screenshot does not mean negative
processing time. The first copy reading preceded the extra browser readers;
other readings were during or after their runs. These sparse readings are
approximate, not a delay distribution or guaranteed maximum.

The late copy screenshot is retained explicitly. Its large clock read **637.69**,
the local picture **631.27**, and the forwarded picture **631.33**: picture ages
of **6.42 and 6.36 seconds**. The camera and forwarding services still reported
ready. The next screenshot recovered to about **0.40 and 0.38 seconds** without
changing settings or restarting a service. The cause was not established. Both
views were affected, so this observation does not isolate the forwarder as the
cause.

This is a concrete gap in the current health display: a connected service does
not prove a recent picture. The next reliability investigation should distinguish
stopped camera capture, delayed incoming data and browser display stalls, and
show an explicit warning when the picture stops advancing. Detecting every old
but still advancing picture will also require a verified source-time signal.

## Decision and retained evidence

The detail observations met the provisional local trial targets: at least 19
decoded fps, no reported freezes in either thirty-second detail browser window,
average forwarding CPU below one core, and no more than about 100 ms behind the
local picture in the sampled screenshots. The late stall prevents a claim that
the overall system keeps picture age bounded.

Use `copy` for the present local baseline and retain `detail` as an explicit
bandwidth-saving choice. A live trial with a controlled connection limit remains
necessary before selecting it for weak networks. Fast motion, small-text
readability, low light, several simultaneous encoders and long-duration stability
remain unqualified by this clock scene. The Larix watermark is present throughout.

[The private evidence index](../reports/live-detail-trial-20260912.json) links
all four browser reports and CPU reports, retains the exact clock triples, and
identifies the excluded preliminary runs. One preliminary scene turned black;
it was not used for the comparison. Private reports and camera footage are not
included in Git. [Settings, rollback and measurement method](live-detail-profile.md).
