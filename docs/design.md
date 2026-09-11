# Why the first lab is built this way

**Design status, 11 September 2026:** Go control, up to four registered sources, SQLite and optional R2 uploads are implemented. Acceptance with two simultaneous generated sources passed all 50 checks, including full decoding of 26 finalized clips after orderly shutdown. Consult the README and test reports for verified behaviour. The [researched constraints and language decision](constraints-and-language-choice.md) supplies target requirements for later stages too.

The first goal is to receive video from several devices, watch and record each source separately, and stop one source's forwarding without stopping its local picture or other sources. A phone, tablet, compatible camera or another computer can be a sender. The first input requires H.264 in MPEG-TS over SRT; additional input types need adapters.

This version runs directly on your Mac, without Docker or a Linux virtual machine. Two separate video-server programs represent the nearby computer and the future remote computer. They share one physical machine, disk, power supply, and network connection.

```text
Source camera-01       Source camera-02
    |                      |
    +----------------------+
    | Local network: compressed live video
    v
Nearby video service (MediaMTX, on the Mac)
    |
    +--> A local browser picture for each source
    |
    +--> A recording program per source (FFmpeg) --> separate recording files
    |
    +--> A forwarding program per source (FFmpeg)
              |
              v
         Second video service (MediaMTX, also on the Mac)
              |
              v
         A forwarded browser picture for each source
```

## Each part has one clear job

**Each sender captures and compresses its picture.** Compression reduces how much data it sends. H.264 is our initial compression format because the selected receiving and browser tools support it. SRT carries the video to the receiving computer, can ask for missing data again within a limited time, and encrypts the media when a passphrase is configured. Larix on a phone or tablet is one sender; a compatible camera or computer can use the same input. [Larix capabilities](https://softvelum.com/larix/ios/), [MediaMTX SRT support](https://mediamtx.org/docs/publish/srt-clients).

**The source registry separates device identity from device type.** A source has a stable ID such as `camera-02`, a readable label, and its own publishing credentials. Setup starts with `camera-01`; `source add` registers another device while the lab is stopped. It writes `.local/connections/<id>.txt` and adds an entry to the secret-free `SOURCES.txt` index. The same source ID identifies its viewer paths, controls and recordings. Replacing a phone with a different compatible sender does not require a new architecture.

**The first MediaMTX service distributes incoming video.** It has a separate path for each source, such as `/camera-01` and `/camera-02`. Local viewers, recorders and forwarders read the appropriate path. Each publisher is permitted to send only to its own path, and a second publisher cannot replace it. MediaMTX handles the connections; resizing and further compression stay in FFmpeg. [MediaMTX introduction](https://mediamtx.org/docs/kickoff/introduction).

**Each source has its own recorder.** It reads from that source's nearby path, so stopping its forwarder should not stop recording. Session folders and database entries retain the source ID. Recorders share a 2 GB disk budget and pause together when that budget is reached or less than 1 GB is free. A recorder can still fail because its input, storage or host fails. Separate programs do not provide independent hardware.

An observed source-disconnect test left an incomplete compressed frame in the final MP4 even though the file had been finalized. The lab retains the received footage without repairing or re-encoding it. A matching checksum proves byte identity, and an archived state confirms the stored copy; neither establishes that every picture can be decoded. Recording-quality detection is not implemented yet. Full decoding after an orderly shutdown must be distinguished from this unexpected-input-loss case.

**Each source has its own forwarder and picture setting.** The `copy` profile forwards existing compressed pictures without another encode. The `small` profile decodes and compresses a smaller version. It can save data but uses computing power, takes time and may remove detail. The received source remains available locally. `./lab --source camera-02 profile small` changes only camera-02's forwarded version. Put the source option before the command. [FFmpeg's explanation of copying and converting video](https://ffmpeg.org/ffmpeg.html#Streamcopy).

**The second MediaMTX service represents remote distribution.** Separating it from the first service gives us a place to interrupt delivery without deliberately stopping the incoming camera stream. In a later stage, this service can run on another computer.

Both MediaMTX processes are shared across sources. Failure of the nearby service affects every source using it; failure of the second service affects every forwarded viewer. Per-source recorder and forwarder processes limit some failures, while shared services and hardware remain common failure points.

**The browser lets us inspect the result.** WebRTC is the browser technology used here to receive live media with a short delay. The two local pages show the source and forwarded picture. Their connection state is not proof of the actual age of the image.

The generated 720p `camera-01` stream displayed in both local and forwarded browser players; [the browser report](../reports/browser-check.json) records that check. It does not prove two-source browser capacity or compatibility with a physical camera.

**The Go control program starts, watches and stops the other programs.** It manages two shared MediaMTX services and each source's recorder, forwarder and optional generated test pattern. A failed status query is treated as unknown, so it does not deliberately stop otherwise working media. Existing workers can keep draining output if a log destination fails. These rules still need failure testing; they do not make every stalled disk operation harmless. An executable is the built program that the computer runs. Building it requires Go tools; running it does not. Video passes through FFmpeg and MediaMTX, so Go does not improve picture quality or encoding speed.

Go, Python, and Java could all do this work. We selected Go for the combination of process control and deployment needs, not because a language happened to be installed. Python's `asyncio` and Java's virtual threads can also manage work that spends time waiting. Our full comparison is in [decision 001](decisions/001-control-language.md).

**SQLite tracks completed recordings and upload progress.** Each entry identifies its source, finished file, checksum and upload state. A transaction groups database changes so they succeed together or leave the previous state. Restart recovery also checks actual files: the database, video files and remote archive do not share one transaction.

The constraint review selected SQLite for durable progress, even though it adds a dependency. JSON remains useful for settings and replaceable status snapshots. The catalog preserves upload history when a local file is moved or goes missing. [R2 setup and recovery behaviour](r2-setup.md) explains the optional archive. A 1,252,488-byte generated recording has been uploaded and confirmed in the configured private bucket; [the report](../reports/latest-r2-check.json) proves that basic path, not all failure cases.

## How this applies Designing Data-Intensive Applications

DDIA helps us choose guarantees and understand their costs. It does not prescribe a camera app or video format. These are our applications of its ideas. The chapter numbers below refer to the **first edition**. [Book overview](https://dataintensive.net/), [first-edition contents](https://www.oreilly.com/library/view/designing-data-intensive-applications/9781491903063/).

| DDIA idea | Plain meaning | What we do in this project |
| --- | --- | --- |
| Reliability, Chapter 1 | Say what must still work when a specified thing fails | Stop forwarding and check that local viewing and recording continue |
| Partial failure, Chapter 8 | One part can fail while another keeps working | Use separate receiving, recording, and forwarding processes |
| Streams and slow consumers, Chapter 11 | New data can arrive faster than a receiver can use it | Treat an old live picture differently from a saved recording; later measure and bound waiting time |
| Storage, Chapter 3; durability, Chapter 7 | A file existing is different from a proven promise that it survives a crash | Save local segments now; later test sudden interruption and recovery |
| Replication, Chapter 5 | Copies on another machine can survive the loss of the first | R2 uploads create a second copy only after confirmation; one real-bucket upload passed, while interruption cases need more testing |
| Transactions, Chapter 7 | Related changes should complete together or be recoverable | SQLite stores recording and upload progress; retries reconcile files, database entries and remote results |
| Partitioning, Chapter 6 | Divide work when one computer cannot carry it all | Sources have stable IDs; later assign whole source streams to different computers after measuring load |

The first important distinction is between **live video** and **recordings**. For live viewing, a frame loses value as it gets older. For a recording, older frames remain the reason the file exists. A single "never drop anything" rule cannot serve both needs well.

The second distinction is between a **running process** and a **working service**. A video program can be running while showing a frozen picture. We must eventually measure the viewer's result, not just process status.

## The security boundary in this version

Source input is available on this computer's local network at one configured SRT port. The Stream ID selects the source path. Each source has a generated username, password and encryption passphrase; its publishing identity is restricted to its own path. Read access, browser pages, the second service and management interfaces are limited to this computer.

Treat `.local/connections/` and generated configuration as private. The username/password authorizes publishing; the passphrase encrypts media. SRT encryption does not promise that all connection metadata is hidden. WireGuard, a tool that creates an encrypted connection between computers, is future work before using this design across untrusted networks. It is not installed or enabled by this lab.

Recordings are local files protected by the Mac's access controls. This lab does not add its own encryption for stored recordings. Users or programs with sufficient access to this Mac can read them.

This is a local learning prototype, not a production-ready system. Public viewers, internet deployment, user accounts, permission removal, and protection against hostile traffic need further design and testing.

## Implemented features and test results

- Up to four source identities can be registered with separate credentials and viewer paths.
- Each source has its own recorder and forwarder.
- `./lab --source camera-01 relay-off` and `relay-on` let us test one forwarding branch while observing the others.
- `./lab --source camera-02 profile small` lets us compare one source's received video with its smaller forwarded version.
- `./lab --source camera-01 demo` supplies a generated source; `demo-stop` releases that path for a real sender.

Two-source generated-video acceptance passed: [the report](../reports/latest-integration.json) records 50 assertions, source isolation, relay recovery and recording persistence across orderly shutdown. Real devices still need connection and playback checks. Four registered sources are an implementation limit, not a measured capacity claim. A generated-source test does not prove a physical device or wireless path has passed.

## What remains to build

1. **Measured video age.** Track how old the displayed camera picture is, with a clear statement of clock error. This version does not measure the full time from camera capture to browser display.
2. **Real network impairment.** Add Linux and `netem`, a tool for deliberately delaying or losing network data. Stopping a forwarding process is a different test.
3. **Separate computers and connections.** Move the receiving and remote services apart. Add a truly separate backup internet connection and measure switching. There is no actual internet-path redundancy today.
4. **Recording durability and archive recovery.** Extend the successful real-bucket upload check with interrupted transfers. Test full-disk and physical power-loss behaviour on suitable separate equipment.
5. **Repeatable quality measurements.** Use the same recorded input for every compression setting, measure the data rate, and compare matching pictures. Looking at two changing live views is only an initial check.
6. **Capacity and independent recovery.** Measure CPU, memory, disk and network work with four sources and an explicit viewer count. Add operating-system supervision of the Go controller. Separate software on one Mac does not protect against that Mac losing power.
7. **Recorded-video health checks.** Detect and label damaged video frames after a source disappears. Keep the original recording, distinguish saved bytes from playable video, and report any missing footage. Current checksum and upload confirmation do not perform that video check.

We will record what each experiment actually proves. A process-restart test cannot establish survival of a power failure, and a local Wi-Fi test cannot establish reliability over a distant internet connection.
