# Observe live forwarding

Run from the project folder while the camera and forwarding process are already running:

```sh
go run ./cmd/relay_check --root . --source camera-01 --duration 30s
```

This command reads the lab and process counters. It does not select a profile,
restart a service, change a camera connection or change uploads. Let a newly
selected profile settle before starting the measurement. The requested duration
must be between 5 and 120 seconds.

The private result is saved as `reports/relay-check-*/result.json`, with file
permissions `0600`. These reports are ignored by Git. They contain an explicit
list of measurement fields, not the lab's full state, credentials, media URLs or
process command lines.

## What the numbers mean

- **CPU:** the forwarding process's accumulated processor time during the
  observed window, divided by elapsed time. One fully occupied processor core
  is 100%; two cores can be 200%. Zero is a valid result at the precision provided
  by `ps`. The camera encoder, receiver, browser, recorder and uploader are
  separate processes and are not included.
- **Memory:** the average and maximum of resident memory samples, in KiB. This
  is sampled memory, not the largest allocation that could have occurred between
  samples.
- **Forwarded payload rate:** the increase in the receiving MediaMTX path's
  `bytesReceived` counter, divided by elapsed time. This excludes some network
  overhead. It is not a count of correctly displayed video frames.

The command samples approximately once per second. A sample reads status and
process counters in a separate process with a 1.8-second limit; each `ps` call
has a 0.5-second limit and returns only process ID, start time, accumulated CPU
and resident memory. The observation window uses a monotonic clock, which does
not jump when the computer's displayed time is corrected. A sample's midpoint
is used for timing; `read_seconds` records the timing uncertainty at each end.

A missing observation, old controller status, process restart, changed profile,
changed connection type, changed publisher identity, backwards counter or
sampling gap makes the **entire measurement inconclusive**. Its aggregate fields
remain `null`, and the command exits unsuccessfully. CPU and payload rates are
never calculated across a restart or only from the surviving samples.

This check does not measure camera-to-screen delay, freezes, text readability
or long-term reliability. A brief interruption between samples can be missed.
Use the filmed-clock and browser measurements alongside it.
