# Bandwidth comparison on 12 September 2026

The smaller 360p/20-fps candidate preserved every scored picture at both tested
connection limits. The 720p/20-fps candidate lost pictures at the 1,500 kbps limit.
An encoder's average target rate is not enough to select a connection size:
packet bursts, packaging and repair traffic also need room.

These results use prepared, generated video sent at normal playback speed on one
Mac. They are not physical-camera, live-encoding or browser-delay measurements.
See [the method and predeclared decision rule](bandwidth-comparison.md).

## Initial comparison

The initial matrix had 18 trials: three encodings, three connection conditions
and two repeats. All 18 simultaneous clean references preserved their complete
scoring window. The limiter measured 1,479 kbps against a nominal 1,500 kbps cap
and 890 kbps against a nominal 900 kbps cap. Both calibrations passed the checks
defined before the run. Rates count UDP payloads, including SRT data and control
traffic, and exclude UDP, IP and physical-network headers.

| Prepared version | Connection cap | Correct scored pictures, each repeat | Outcome |
| --- | --- | --- | --- |
| Original 720p / 30 fps | Unlimited | 270/270; 270/270 | Met the rule |
| Original 720p / 30 fps | 1,500 kbps | 0/270; 0/270 | No intact scored pictures; timing inconclusive |
| Original 720p / 30 fps | 900 kbps | 0/270; 0/270 | No intact scored pictures; timing inconclusive |
| 720p / 20 fps | Unlimited | 180/180; 180/180 | Met the rule |
| 720p / 20 fps | 1,500 kbps | 160/180; 160/180 | Did not meet the rule |
| 720p / 20 fps | 900 kbps | 0/180; 0/180 | Did not meet the rule |
| 360p / 20 fps | Unlimited | 180/180; 180/180 | Met the rule |
| 360p / 20 fps | 1,500 kbps | 180/180; 180/180 | Met the rule |
| 360p / 20 fps | 900 kbps | 180/180; 180/180 | Met the rule |

Every row marked “Met the rule” delivered all expected pictures within 250 ms
of the identical encoded pictures on its clean reference, with no gap above
150 ms between correct decoded pictures. This is added delay against that
reference, not total picture age.

Four original-video trials had no usable picture-to-timestamp mapping after the
connection became overloaded. Their timing and missing-versus-damaged breakdown
remain inconclusive; the count of exact scored matches was zero. The command
therefore exited with an explicit “4 of 18 trials were inconclusive” result.
The other 14 trials passed measurement validity checks, including trials that
measured failed delivery. No simulator resource drops occurred. Intentional
drops from full connection queues remained separate from simulator failures.

The generated source file averaged about 2,037 kbps, the full-size candidate
1,221 kbps, and the smaller candidate 663 kbps. These are file bits divided by
twelve seconds, including MP4 packaging; they are not network throughput.
The 720p/20-fps result at 1,500 kbps illustrates why average size alone does not
establish that complete pictures will arrive before the repair deadline.

Private evidence on the development computer is in
[the initial report](../.local/diagnostics/bandwidth-2062182321/results.json)
and [its readable table](../.local/diagnostics/bandwidth-2062182321/results.md).
These ignored files and the generated media are not included in a fresh clone.

## Follow-up with more room for 720p

After the initial failures at 1,500 kbps, a four-trial follow-up sent one exact
prepared 720p/20-fps file at both 1,500 and 2,000 kbps, with two repeats each.
The acceptance rule, 32,768-byte queue, 120 ms repair allowance and fixed scoring
window were unchanged. Reusing the exact candidate matters because preparing
compressed video again can produce different bytes and decoded pictures.

The limiter calibrated at 1,474 kbps and 1,953 kbps respectively. All four
simultaneous clean references preserved their complete 180-picture window, and
all four timing measurements were conclusive.

| Nominal connection cap | Correct scored pictures, each repeat | Outcome |
| --- | --- | --- |
| 1,500 kbps | 160/180; 160/180 | Did not meet the rule |
| 2,000 kbps | 180/180; 180/180 | Met the rule |

At 2,000 kbps, every scored picture arrived within 250 ms of its clean reference.
Maximum gaps between correct pictures were 62.5 and 61.6 ms. Neither repeat had
congestion drops or simulator resource drops.

This establishes a successful short test point, not the minimum bandwidth for
720p. [Private follow-up evidence](../.local/diagnostics/bandwidth-1029672886/results.json)
and [its readable table](../.local/diagnostics/bandwidth-1029672886/results.md)
are retained on the development computer.

## What this permits next

The 360p candidate qualifies for a live trial under these tested conditions.
Its successful delivery does not establish that small text remains readable.
The real-camera compression comparison showed softer keyboard lettering at
360p, so useful detail must remain part of the decision.

The 720p candidate also qualifies for a live trial with the greater connection
capacity tested in the follow-up. It has not qualified at the 1,500 kbps limit.

The next live trial must measure camera-to-screen delay, visible stalls and
continuous forwarding CPU use while keeping the camera and viewer settings
fixed. These prepared-file tests measure none of those quantities. Two short
repeats do not establish long-term reliability or performance on a radio link.
