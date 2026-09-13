# Compare compression on a saved recording

`qualitycheck` makes two smaller-video candidates from one completed recording. It shows their actual file sizes, picture similarity and processing cost, with a local page for watching the differences. It preserves the received recording and does not change camera settings, live forwarding profiles or upload history.

The saved recording is already compressed video that reached this computer. It is the reference for this experiment, not an uncompressed camera original. No measured compression results are claimed in this guide.

## Run a comparison

Run from the project folder after normal setup. Go is needed for `go run`; FFmpeg and FFprobe must be available at the paths saved by setup. FFmpeg needs the `libx264` encoder and `ssim` filter.

```sh
go run ./cmd/qualitycheck --recording 'SESSION/FILE.mp4'
```

Replace `SESSION/FILE.mp4` with the exact relative filename of a completed MP4 in the recording catalog. Omit the leading `recordings/`. An arbitrary file, URL or unfinished recording is not accepted. If you have used the recording-health checker, `./lab recordings health --json` includes its checked recording paths.

The command creates a private folder under `reports/quality-run-*`, prints a summary and starts the comparison page at <http://127.0.0.1:19082>. Open the printed address on this computer. To use another project folder, add `--root /path/to/field-video-lab`. If port 19082 is occupied, add `--listen 127.0.0.1:19083`.

To create the files and exit without starting a page:

```sh
go run ./cmd/qualitycheck --recording 'SESSION/FILE.mp4' --no-serve
```

To reopen a completed comparison without encoding it again:

```sh
go run ./cmd/qualitycheck --report-dir '/path/to/field-video-lab/reports/quality-run-EXAMPLE'
```

Use the actual folder printed by the earlier run. Choose either `--recording` or `--report-dir`. Adding `--no-serve` when reopening validates the files, prints their saved summary and exits. Stop the page with Ctrl+C; the report files remain available.

The diagnostic does not start or stop media services. It does use CPU and disk, so it can compete with ongoing recording and uploads. One comparison can encode at a time per project; viewing a completed report releases that lock.

## What it creates

| File | Picture size | Pictures per second | Video data-rate target |
| --- | --- | --- | --- |
| `original.mp4` | Received recording's dimensions | Received recording's rate | Unchanged copy |
| `playback.mp4` | Received recording's dimensions | Received recording's rate | Same compressed frames, timing starts at zero |
| `detail-20.mp4` | Same dimensions as the recording | 20 | 1,200 kilobits per second |
| `small-20.mp4` | 640 × 360 | 20 | 650 kilobits per second |

Both new versions use H.264 Baseline with no B-frames, the `veryfast` encoder preset and a keyframe every second. A keyframe can be decoded without an earlier picture. These settings make a repeatable saved-file experiment; they do not select a live profile automatically.

Data-rate targets are encoder settings. The reported file sizes are the bytes actually written, including the MP4 container. A candidate can be larger than an already efficient source recording; the report shows an increase when that happens.

The folder also contains `result.json` and one per-frame SSIM log for each candidate. The JSON records creation time, FFmpeg version, dimensions, frame counts, timing, sizes, similarity and SHA-256 checksums for the four MP4 files. Keep the entire folder private because it contains copies of footage.

Some recordings start their internal clock above zero. The browser plays `playback.mp4` in the original-picture panel: a copy with the same compressed video and a clock starting at zero. This step changes the MP4 packaging without compressing the picture again. The tool checks every decoded picture against the original and verifies that frame timing differs only by the starting offset. File-size comparisons and SSIM still use the unchanged `original.mp4`.

## Read the quality result

**SSIM**, short for Structural Similarity, compares the patterns in two pictures. A score of 1 means identical compared pixels. Treat it as a similarity index; it is not a percentage of quality retained, and it does not establish whether important text remains readable. FFmpeg requires matching picture dimensions, pixel format and corresponding frames. [Official FFmpeg SSIM documentation](https://ffmpeg.org/ffmpeg-filters.html#ssim).

This tool applies the same 20 fps frame-selection rule to the original for both comparisons. It checks candidate frame counts and timestamps against that reference. It compares at the original picture size, enlarging the smaller candidate with bicubic scaling: a fixed method that estimates the pixels between its existing pixels. Both comparisons use the same pixel format. The final score is the average of the complete per-frame results; incomplete, invalid or misaligned evidence causes an error.

Removing pictures also removes motion samples. A 30 fps source converted to 20 fps has fewer recorded moments each second. **The SSIM score covers the selected moments only; it does not score the motion that was removed.** Watch moving objects as well as small details. One high average can also hide a worse section of the clip.

For an input below 20 fps, the conversion can repeat pictures to reach 20 fps. Repetition does not create new captured moments.

On the page, select a candidate, then use **Play both**, pause and the position slider to inspect it beside the original. Browser playback and seeking are approximately aligned; they are not the exact frame alignment used by the offline score. A saved score and a visual check answer related but different questions.

## Read the processing result

**Time to encode** is elapsed clock time for the candidate's conversion command. It includes reading and decoding the saved input, filtering, compression and writing the MP4. **CPU time** is the processor time charged to that command. With several threads working together, CPU time can exceed elapsed time.

These numbers describe this computer and this run. They exclude other comparison stages and are not live camera-to-screen delay. A quick saved-file conversion does not prove that the same settings will stay fast during continuous streaming, network trouble or heavier load.

## Input and resource limits

The first version accepts the lab's narrowly defined recording format:

- One video-only H.264 stream in an MP4 file, between 1 and 15 seconds and no larger than 64 MiB.
- A 16:9 picture from 640 × 360 through 1920 × 1080, with even dimensions, progressive pictures and the common `yuv420p` pixel format.
- Square pixels. If the recording does not declare its pixel shape, the tool assumes square pixels and records that assumption in the report.
- At most 900 decoded frames and about 60 frames per second, with consistent, increasing timestamps. The whole input must decode without reported errors.
- At least 2 GiB of free space when starting, leaving room for this run and ongoing recordings.

The two conversions run sequentially. FFmpeg uses bounded decoder/encoder thread counts, single-threaded filters and a 45-second limit per child command. Media comparison has a two-minute deadline within a three-minute limit for copying and processing. Captured command output and individual FFmpeg allocations are bounded.

These limits reduce resource use; they are not a hard total-memory limit or a process sandbox. The free-space check does not reserve disk space against other programs. A failure can leave partial files in that run's private folder; only a completed, validated `result.json` can be reopened as a comparison.

## Original files and access

The diagnostic opens SQLite in read-only, query-only mode. It looks up the selected file's recorded size and checksum without migrating the catalog or updating recording, health or archive state. It opens the selected path inside the recording directory, checks that it is a regular file and copies it into a new private folder. The copy must match the catalog's size and SHA-256. All decoding and compression then use that copy, whose size and checksum are checked again after processing.

New output files cannot overwrite the original. Reopening a report requires its expected fields and verifies each MP4's size and checksum. These checks detect mismatched or changed files; the hashes do not authenticate who created the report or prove visual quality.

The page accepts only a loopback address, meaning this computer. It serves a fixed list of report and video files, allows reading only, checks the requested host and blocks unrelated file paths. It does not expose the recording directory, settings or catalog as a file browser. The media commands allow local file/pipe inputs and the MP4 reader; the diagnostic does not contact the archive. It assumes a trusted local computer and its installed media tools.

## Connection to DDIA

This is our application of ideas from *Designing Data-Intensive Applications* about source data, derived data and batch processing. The catalog identifies the received recording. Candidate videos and similarity reports are derived results that can be rebuilt from that recording with the same tool settings. Copying and identifying the input first gives both candidates the same data to work on.

Keeping the diagnostic separate also lets the recording and upload workflow continue independently. A failed comparison requires another local calculation; it should not rewrite the original or change upload history. One writer and one small JSON report are enough for this bounded command. The book does not prescribe these bitrates, SSIM, Go or this storage format. See [the wider design notes](design.md).

## Test plan and limits

Run the focused tests with FFmpeg and FFprobe installed:

```sh
go test ./cmd/qualitycheck -count=1 -v
node --test cmd/qualitycheck/page_test.mjs
```

The real-media cases use generated clips in temporary folders and skip explicitly if the media tools are missing. They check unchanged source bytes, output dimensions and frame counts, full reference coverage, an identical-picture control, incorrect timing, different pictures and truncated inputs. Other tests cover catalog/history preservation, older schemas, missing or changed files, path escapes, report completeness, checksums and local HTTP access. Page tests cover safe media paths, size increases, similarity labels and shared playback bounds.

For Go changes, also run the repository checks:

```sh
go test ./...
go vet ./...
```

For a manual review, choose a completed clip containing readable small text and movement. Run the command, compare both candidates, inspect the report and reopen it with `--report-dir`. Repeat on other scenes before selecting a live setting: low light, fine texture and fast movement can compress differently. Check continuous processing load and live delay separately before applying a profile to a running source.

A single clip cannot establish general image quality, long-term capacity, network resilience or production reliability. This diagnostic also cannot recover footage that never arrived, detect an entire missing scene, validate audio or verify an R2 object. [Recording-health checks](recording-health.md) remain a separate way to test whether saved bytes decode.
