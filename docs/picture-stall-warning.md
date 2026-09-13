# Historical browser picture-progress warnings

The live browser players and their warning code have been removed after adopting
GStreamer. **The current native viewer has no picture-progress warning.** The
standalone clock page only displays a target to film. Use the
[manual GStreamer clock method](gstreamer-viewer.md#make-a-fair-comparison) for
current picture-delay checks.

This note preserves findings from the browser implementation. Its complete
[design, code and test instructions at commit 40dfbc8](https://github.com/MartinKalema/fieldstream/blob/40dfbc8/docs/picture-stall-warning.md)
describe a retired version, not commands to run in the current checkout.

## What the old warning measured

Each player checked whether its presented video count and video timestamps
advanced. After 1.5 seconds without progress while observation was available,
it warned that picture progress had stopped. Recovery required one second of
advancing pictures. Paused, hidden, ended and unavailable observations had
separate states. The page retained only its latest 12 state changes in memory.

An open connection was not evidence of an advancing picture. Advancing video
timestamps were not proof of a recent camera picture: a frozen camera could
send the same image with new timestamps, and moving video could remain delayed.
A motionless scene was not treated as a failure just because its pixels matched.
The 1.5-second threshold was an observation rule, not a guaranteed alarm deadline.

## Browser observations on 12 September 2026

An isolated generated-video fixture used real browser video connections and
presentation callbacks. It did not contact the physical camera or R2.

| Controlled action | Observed result |
| --- | --- |
| Hold forwarded frames while its connection stays open | Forwarded warning; local picture kept advancing |
| Resume forwarded frames | Recovery check, then advancing; earlier interruption retained |
| Hold both pictures | Separate warnings while both connections stayed open |
| End forwarded connection | Check unavailable; local picture continued |
| Reconnect forwarded viewer | Fresh observations, then advancing; earlier interruption retained |
| Pause forwarded playback while the source keeps sending | Playback paused, with the connection still open |
| Resume playback | Recovery check before the advancing state returned |

The former comparison page also received both real camera routes after the
user restarted broadcasting. Both reached the advancing state. Private evidence
is retained in `reports/picture-stall-warning-20260912.json` and excluded from Git.

Hidden-page handling, missing callbacks, timestamp resets and threshold
boundaries were checked with controlled inputs. These checks did not identify
the cause of an earlier six-second camera delay, prove all stale pictures could
be detected, or establish long-term availability. They do not validate the new
native viewer.

## Requirements for a future native warning

- Observe picture progress close to native display, independently of process
  lifetime, an open connection and receiver status.
- Keep a normal still scene distinct from missing pictures. Do not claim that
  advancing timestamps establish camera capture time.
- Report unavailable observations and interruptions explicitly. Define recovery
  and retained event history, then test them against controlled freezes.
- Test the displayed warning with a real native window, including slow or
  suspended display work. Decoder or network counters alone are insufficient.

The design lesson from DDIA's discussion of unreliable networks and clocks
still applies: state what each observation can establish, and keep unknown
conditions visible. This is our application of that reasoning, not a claim that
the book specifies a video warning implementation.
