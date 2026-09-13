# ADR-002: Check recorded video separately from upload confirmation

**Status:** Accepted for this local lab milestone
**Date:** 2026-09-12
**Deciders:** Fieldstream project implementation; review tracked in the feature PR

## Context

A camera disconnect previously left a damaged compressed frame inside a finalized MP4. The file could still be hashed and uploaded. Those operations confirmed the saved bytes, but did not establish whether its pictures could be decoded.

The current machine receives video, records it and uploads it. Decoding every saved recording automatically would add continuous CPU work. We have not yet measured enough spare capacity to enable that safely by default. The checker must preserve footage, avoid network access, leave upload decisions unchanged and make an unfinished check distinguishable from a failed decode.

## Decision

Add `./lab recordings check` for an explicit batch of finalized, cataloged recordings, and `./lab recordings health` for dated results. The default batch is 20 files. `--limit` accepts 1–1000; `--source` can restrict a batch to one source. Only one checker runs at a time.

Use the existing FFmpeg installation to decode the first H.264 video track into a null output, which discards decoded frames. Go handles safe file opening, checksums, process cancellation and report persistence. This adds no language, service or package dependency.

Require successful decoder completion, positive frame count, a final progress marker and no error diagnostics before reporting `decodable`. FFmpeg's strict error and empty-output options help make a partial decode fail. [FFmpeg options](https://ffmpeg.org/ffmpeg.html#Advanced-options).

Pass an already opened regular-file descriptor to FFmpeg. Allow only its `fd` input protocol, force the MP4 reader, and disable external tracks. This avoids asking FFmpeg to open a user-provided URL or follow a reference to another file. The `fd` protocol permits seeking inside a regular file. [File descriptor protocol](https://ffmpeg.org/ffmpeg-protocols.html#fd), [MP4 external-track options](https://ffmpeg.org/ffmpeg-formats.html#mov_002fmp4_002f3gp).

Verify the catalog's size and SHA-256 before and after decoding. Limit each recording to 64 MiB, each check to a 30-second deadline, decoder/filter thread counts to one, individual FFmpeg allocations to 64 MiB and picture size to 8,294,400 pixels. Captured output is bounded. These controls are not a hard total-memory limit or a process sandbox.

Save results in private `.local/video-health.json`, using the existing flush-and-rename writer after each completed check. The recording identity, tool version, frame count and check time accompany each result. A crash before saving can require repeating one check. Existing results are skipped unless `--recheck` is supplied.

## Options considered

| Option | Complexity and cost | Scaling and familiarity | Reason |
| --- | --- | --- | --- |
| Check during recording finalization | Simple ordering, but decoding delays completion | Competes with the live recorder; uses familiar FFmpeg | Rejected: recording progress should not wait for a health result. |
| Always run a separate queue worker | Needs scheduling, restart recovery and load measurements; continuous CPU use | Could grow to several workers later | Deferred until we measure spare capacity and queue growth. |
| Explicit bounded batch with a saved report | One additional command and rebuildable report; CPU work only when requested | One worker; existing Go and FFmpeg tools | Selected for the present local lab. |

## Trade-off analysis

SQLite remains the source of recording identity and upload progress. The health report is derived from those identities and the original files: it can be rebuilt while the files remain available. Keeping this report separate avoids a schema migration or decoder-held transaction in the running recorder's catalog. Its cost is that a report can be older than the catalog and is not joined automatically into the live status snapshot.

This applies DDIA's distinction between source data and derived results. Losing an upload-progress record can create uncertainty about external work; losing a decode report requires repeating a local calculation. They do not need the same storage workflow. The command reconciles saved results with catalog identity before presenting counts. [DDIA book overview](https://dataintensive.net/).

An atomic JSON report is sufficient for a small, single-writer diagnostic. Rewriting it after every result is linear in report size. Inventory is capped at 100,000 entries and the report at 64 MiB; large deployments need indexed health storage and incremental updates. This design is not a claim that a large JSON history scales indefinitely.

## Consequences

- Saved, archived and decodable are independent facts. An upload does not make a decode error disappear.
- Original bytes are neither repaired nor deleted. Uploads may still preserve footage with decode errors.
- `decode_error` means the configured decoder could not fully decode the video. It does not by itself identify whether the cause was damaged data or unsupported media.
- `check_failed` covers missing or changed files, timeouts, tool startup/configuration problems and incomplete checking. It must not be presented as proof that the video is damaged.
- `decodable` is a dated observation. It does not detect missing scenes, an entire missing segment, frozen or blurred pictures, acceptable compression quality, or later removal from R2.
- Only the first H.264 video track is checked. Audio and other codecs need separate support.
- No automatic worker, archive-download check, playback UI or recording repair is introduced in this milestone.

## Action items

1. [x] Add bounded decoding, distinct outcomes and private persisted results.
2. [x] Test complete, truncated and damaged video using real FFmpeg.
3. [x] Test cancellation, missing/changed files, path escape, batch restart and preservation of upload history.
4. [ ] Measure ongoing CPU and queue growth before enabling automatic checking.
5. [ ] Add recording continuity and archive-download checks separately.
