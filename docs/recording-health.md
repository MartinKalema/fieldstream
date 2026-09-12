# Check whether saved video can be decoded

The lab keeps three different facts separate:

| Fact | Meaning |
| --- | --- |
| Saved | A completed local file was entered in the catalog. |
| Archived | Its upload was confirmed at that time. |
| Decodable | FFmpeg read its video without reporting errors at the recorded check time. |

A damaged file can be copied perfectly. That is why its checksum and R2 confirmation do not replace this check.

## Run a batch

```sh
./lab build
./lab recordings check
./lab recordings health
```

The default checks up to 20 previously unchecked recordings, one at a time. Run it again to work through the next batch. It reads the completed-file catalog; an unfinished MP4 outside that catalog is not eligible. The command does not start, stop or reconnect camera services. It does use CPU and disk, so a running camera can still compete for those resources.

For another batch size, one source, or a fresh check of earlier results:

```sh
./lab recordings check --limit 5
./lab --source camera-01 recordings check --limit 20
./lab --source camera-01 recordings check --limit 20 --recheck
./lab recordings check --path SESSION/FILE.mp4 --recheck
./lab recordings health --json
```

`--source` goes before `recordings`. An unfiltered batch considers every source. `--recheck` starts from the first matching catalog entries again; it is for deliberately repeating checks. A normal batch skips all previously checked outcomes, including checks that could not finish. Use `--recheck` after correcting a missing file or tool problem. To repeat one particular result, replace `SESSION/FILE.mp4` with its exact relative filename from the report. The path must already be in the selected catalog.

The JSON output contains the complete saved result list for the selected catalog entries. Plain text limits problem details to 20 entries. Results are stored privately in `.local/video-health.json` and survive command restarts. They are dated observations, not continuous monitoring. `./lab status` still reports the live services and archive; use `recordings health` for these separate checks.

## Read the result

| Result | What to do with it |
| --- | --- |
| `decodable` | The checked bytes decoded without reported errors. This does not establish that all expected footage arrived. |
| `decode_error` | FFmpeg could not fully decode the selected video. Keep the original and investigate the recording or unsupported media. |
| `check_failed` | The check could not finish, for example because the file changed, was missing, or exceeded the time limit. Fix the cause and recheck. |
| Unchecked count | No saved result matches this recording's catalog identity yet. |

An archived file with a decode error stays archived, because the archive did copy its original bytes. The checker never deletes, repairs or replaces recordings, and it never changes upload history.

Each result includes its source, relative filename, checksum, checked time, FFmpeg version and number of frames decoded before completion or error. A stored result remains a historical observation if the file later goes missing. Catalog missing-file counts are displayed separately.

## Limits and verification

The checker handles the lab's first H.264 video track in MP4. It requires an FFmpeg build with the `fd` protocol; this development run used FFmpeg 8.0.1. A check has a 30-second deadline and a 64 MiB input limit. It uses one decoder thread and one filter thread. It does not download from R2 or check audio.

The real-media tests generated a complete 12-frame clip, a version with destroyed compressed pictures but an intact MP4 index, and a version cut short after its index. The complete version decoded all 12 frames; the truncated version decoded four and then failed. A non-video file also failed. Tests verify that the original bytes remain intact, that incomplete checks cannot report success, that later batches resume without rechecking completed work, and that archive history is unchanged.

Run the focused checks with FFmpeg installed:

```sh
go test ./internal/lab ./cmd/video_lab -run 'TestVideoHealth|TestRecordings' -count=1 -v
```

The real-media cases explicitly skip if FFmpeg is absent; they need a build with the `libx264` encoder to generate fixtures. These file-decoding tests need no running camera or media server. They do not change the live recording/forwarding pipeline, and are separate from `./lab test`, which requires that pipeline to be stopped.

[Design decision and DDIA connection](decisions/002-recording-health.md).
