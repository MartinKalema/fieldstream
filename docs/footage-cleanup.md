# Removal of old test footage

On 12 September 2026, the user requested deletion of old recordings from both this Mac and Cloudflare R2. The fixed cutoff was **01:49:12 East Africa Time** (2026-09-11T22:49:12Z). Recordings made after that cutoff were kept.

The completed cleanup removed:

- 763 finished camera recording files and their main catalog entries, totalling 807,760,019 bytes.
- 205 older generated or diagnostic video files, totalling 250,888,946 bytes.
- 666 matching R2 objects, totalling 715,129,230 bytes, including the original generated R2 upload check.

The local total was **968 files and 1,058,648,965 bytes**. JSON measurement reports, screenshots, source code, private connection settings and credentials were retained. Historical reports still describe their original successful measurements, but their deleted video files and R2 objects are no longer available for replay.

The cleanup used a fixed manifest. Local paths, sizes and checksums were checked before deletion. Remote keys were derived from the catalog's recording identity; the one older upload-check object required its separate report and catalog proof. Unknown remote objects were excluded. The controller and uploader were stopped briefly and their locks acquired to prevent an upload racing with deletion. Uploads completed between preview and execution were matched and included.

After deletion, a new R2 listing contained none of the selected keys. All selected local paths were absent, and the main catalog contained no pre-cutoff entries. A subsequent local check found 112 newer clips, including an unfinished clip, still present. The lab then restarted with the same source settings; recording and uploads resumed.

This was a one-time requested cleanup. It did not enable automatic deletion or change the recording budget. Deleting footage frees storage; it is not itself a reduction in live video delay.

[Exact manifest](../reports/footage-cleanup-manifest.json) · [Deletion result](../reports/footage-cleanup-result.json)
