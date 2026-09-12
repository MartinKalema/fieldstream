# Connect the recording archive to Cloudflare R2

The live feed works without R2. Your supplied credentials have been saved privately in `.local/r2.json`. A separate private bucket named `field-video-lab` has been created for this project.

## Prepare the bucket and credentials

1. In your Cloudflare account, create a private R2 bucket or choose an existing project bucket. This version uses the normal R2 account endpoint; buckets restricted to a particular jurisdiction need endpoint support added before use. Keep public access disabled. [Cloudflare bucket instructions](https://developers.cloudflare.com/r2/buckets/create-buckets/).
2. Create an R2 API token with **Object Read & Write** permission, restricted to that bucket. The uploader writes objects and reads their saved properties to confirm them. It does not need permission to administer buckets. Save the generated **Access Key ID** and **Secret Access Key** locally. These are R2 S3-compatible credentials, not the general Cloudflare API token string. [Cloudflare R2 authentication](https://developers.cloudflare.com/r2/api/tokens/).
3. Find the Cloudflare account ID associated with the bucket.

Keep access keys out of chat, screenshots, shared documents and Git.

## Configure the lab

Run:

```sh
./lab archive-config
```

This creates `.local/r2.json` with private file permissions, unless it already exists. Existing credentials are never overwritten by this command. Edit the file locally:

```json
{
  "enabled": false,
  "account_id": "YOUR_ACCOUNT_ID",
  "bucket": "your-existing-bucket",
  "access_key_id": "YOUR_R2_ACCESS_KEY_ID",
  "secret_access_key": "YOUR_R2_SECRET_ACCESS_KEY",
  "prefix": "field-video-lab",
  "bytes_per_second": 125000
}
```

After filling in the values, change `enabled` to `true`. Restart the lab to load the new settings:

```sh
./lab stop
./lab start
./lab status
```

Enabling the archive allows the uploader to send **all pending finalized recordings in this project's catalog**, including recordings made before it was enabled. Integration-test footage lives in separate test folders and is not part of that catalog. The uploader does not delete local recordings.

To disable uploads, set `enabled` to `false` and restart. Stopping the lab cancels an active upload; it will retry safely after the next enabled start. An upload that is already complete stays in R2.

## Understand the speed limit

The default is **125,000 bytes per second**, approximately **1 megabit per second** of upload payload. One upload runs at a time. This is a fixed limit; the program does not yet measure spare connection capacity or automatically prioritize live video.

A camera producing 2 megabits per second creates recordings faster than this default uploader sends them. The waiting data grows during continuous recording. Uploads can catch up while the camera is stopped, or you can increase the cap when the connection has sufficient capacity. Protocol overhead also consumes network capacity.

`./lab status` shows archived recordings, waiting data and errors. It also reports recordings whose local file is missing and which have no confirmed archive copy.

## What upload confirmation means

The uploader checks the local file against its recorded SHA-256 value. That value identifies the exact bytes we intended to save. It sends the file over HTTPS with Content-MD5, which R2 supports for checking transfer corruption, then checks the saved object's size and SHA-256 metadata before marking it archived. R2 does not support every Amazon S3 checksum option, so the implementation uses its documented subset. [R2 API compatibility](https://developers.cloudflare.com/r2/api/s3/api/).

Object names include the recording session, filename and content checksum. Retrying an interrupted or unacknowledged upload uses the same name. Changing the configured destination is rejected while the catalog contains an established archive destination; migration needs a deliberate separate operation. Rotating credentials for the same destination is supported through a restart.

This version uploads individual segments up to 64 MiB and reads them in small pieces, rather than loading entire files into memory. Oversized or changed files produce an archive error. Authentication and transfer errors are reported without exposing credentials.

"Archived" records successful confirmation at upload time. The lab does not continuously audit R2 for later deletion or changes, manage remote retention, or provide an archive viewing interface.

The checksum confirms the bytes that were saved; it does not prove every video frame is complete. Disconnect tests found that a sender can disappear partway through its final compressed frame. The MP4 file can be finalized but contain a damaged final frame. This version preserves and uploads those original bytes, without repairing or re-encoding them, and does not yet attach a video-health result to each archive entry. Orderly controller shutdown stops the recorder before its generated sender, avoiding that particular boundary during the normal acceptance test.

## Validation status

Automated local tests exercise signed upload requests, checksum checks, interrupted acknowledgements, retries, missing files and cancellation. On 11 September 2026, a real 1,252,488-byte generated video was uploaded to your private `field-video-lab` bucket using the supplied credentials. The uploader confirmed its saved size and checksum metadata before marking it archived. The result is recorded in `reports/latest-r2-check.json`; the test used a separate catalog and object prefix.

The [process-crash recovery tests](archive-recovery.md) use a local HTTP store and force the uploader to die at three observed boundaries. Another process must recover the unfinished database row, send the same bytes to the same object name, and confirm success. A further restart must preserve that result without another request. These tests exercise the real SDK and SQLite without contacting R2.

The normal lab also saved 16 generated recording pieces. All passed a complete decoding check after orderly shutdown. After restarting the controller, the uploader resumed the pending work and confirmed all 16 in R2, leaving no pending recordings. See `reports/browser-recording-check.json` and `reports/archive-resume-check.json`.

These checks cover real uploads and pending-work recovery after a controller restart. They do not yet prove recovery during an actual internet outage or playback from the archive. The general Cloudflare administration token was used for bucket setup and was removed from the local setup file afterward; the running uploader uses only the R2 access key and secret.
