# fieldstream

Live video streaming with low delay, local recording, and cloud archiving. The development workspace and command-line tools use the name Field Video Lab.

This repository contains the source, tests, build scripts and design notes. Credentials, installed tools, recordings and measurement files under `reports/` and `.local/` stay private on the development computer. Links to those generated files describe local evidence and will not resolve in a fresh clone. Measurements below describe the tested development setup; run setup and the relevant checks for a new installation.

Receive live video from several devices, watch each source locally, record it, and forward a separate copy. A source can be a phone, tablet, compatible camera or another computer. The sender needs H.264 video in MPEG-TS over SRT; other connection types need an adapter before they can join this version.

The lab supports **up to four registered sources**. Each has its own credentials, viewer addresses, recorder and forwarding controls. Two video services run on one Mac and share its CPU, disk, network and power. This workspace currently has `camera-01` and `camera-02` registered. Two-source generated-video acceptance passed all 85 checks. Real devices and four-source capacity still need their own checks.

A 1,252,488-byte generated recording was uploaded and confirmed in the private R2 bucket. See [the R2 check report](reports/latest-r2-check.json). R2 is now enabled in this workspace: normal recording will also queue those files for upload when the lab starts.

Old footage, including that historical R2 test object, was deleted at the user's request on 12 September 2026. Newer footage and measurement reports were retained. [Cleanup scope and verification](docs/footage-cleanup.md).

The local and forwarded browser players displayed the generated 720p `camera-01` stream; see [the browser check](reports/browser-check.json). That check does not establish two-source browser load, physical camera compatibility or camera-to-screen delay.

Start with the researched [constraints and language decision](docs/constraints-and-language-choice.md). The selected design uses Go for service management and uploads, SQLite for durable progress, and Cloudflare R2 for the recording archive. The wider network and power-failure experiments remain later stages.

## Start here

Open Terminal in this folder:

```sh
./lab build
./lab setup
./lab source list
./lab source guide camera-01
./lab start
```

A fresh setup creates the first source, `camera-01`; repeating setup keeps existing sources. Open **SOURCES.txt** for the guide index. Private instructions for the first source are in **.local/connections/camera-01.txt**. The `source guide` command prints that file's location without printing its password.

Put the source device and receiving computer on the same trusted local network. Enter the connection values from the private guide, then start sending. **Larix Broadcaster** is one suitable sender for a phone or tablet; the receiver does not require a particular device brand. Once setup is complete, the Mac's **Start Video Lab.command** launcher can also start the lab.

If this computer changes networks, stop the lab and run `./lab setup` again. Source IDs and passwords are kept. Update the address in each sender. A migrated single-source setup retains its credentials while changing the old `ipad` path to `camera-01`; update that sender's Stream ID from its new guide once.

Read [the source setup guide](docs/source-setup.md) for compatible senders and connection details.

Use [the diagnostic tools guide](docs/developer-tools.md) to run the local-versus-forwarded clock page, compare browser buffering, or generate private Larix connection QR codes from this checkout.

## Add another device

Register a separate source for each device while the lab is stopped. The example below applies when `camera-02` does not already exist; use `source list` first. This workspace already has both example sources.

```sh
./lab stop
./lab source add camera-02 --label "Second camera"
./lab source guide camera-02
./lab source list
./lab start
```

Configure that device using `.local/connections/camera-02.txt`. Both devices can send at the same time. Each password authorizes only its assigned source path. Source IDs start with a lowercase letter and contain up to 32 lowercase letters, digits or hyphens. The four-source limit bounds this implementation; it does not prove every computer can handle four streams at every setting.

## Two viewers per source

- Local picture: <http://127.0.0.1:18889/camera-01>
- Forwarded picture: <http://127.0.0.1:28889/camera-01>

Use `/camera-02` for the second source, or run `./lab source list` for every address. Open viewers on **this computer**. `127.0.0.1` means the computer running the browser; another device cannot use these addresses to reach the lab. Only incoming source video is exposed on the local network.

## Try the experiments

Put `--source` **before the command**:

```sh
./lab status
./lab --source camera-01 status
./lab --source camera-01 relay-off
./lab --source camera-01 relay-on
./lab --source camera-02 profile small
./lab --source camera-02 profile copy
./lab --source camera-01 relay-wait 120
./lab --source camera-01 relay-wait 300
./lab --source camera-01 relay-link local
./lab --source camera-01 relay-link srt
```

Without `--source`, control commands select the first configured source. `status` normally shows every source. `start` and `stop` apply to the whole lab.

`relay-off` stops only the selected source's forwarding program. Its local picture and recording should continue, as should the other sources. This is a forwarding-process experiment; it does not simulate real packet loss or an unplugged cable.

`profile small` changes only the forwarded picture to 640 × 360 at 20 pictures per second, targeting about 650 kilobits per second. It uses more processing than forwarding unchanged video. `profile copy` restores the incoming compressed video without another video encode. A profile switch briefly interrupts the forwarded picture.

The selected source's incoming video and local recording are unchanged by the remote profile selection. Other sources keep their own settings. Audio is left out of forwarding and recording in this version.

`relay-wait` changes how long the selected SRT forwarding connection allows for recovering missing data. The default is **300 milliseconds**; **120 milliseconds** is available for comparison on a stable connection. Changing it briefly reconnects an active SRT forwarder. It does not change the camera app's separate waiting time, the picture size, or other sources. A shorter wait can show pictures sooner but leave damaged or missing pictures when data arrives too late. See [the network experiment](docs/network-delay-test.md) for the measurement method and limits.

In this workspace, camera-01 uses local forwarding with the copy profile; its SRT forwarding allowance is saved as 120 ms for rollback. Its camera connection now uses **80 ms as an experiment on the current local network**. The fresh connection established on 12 September 2026 at 02:14:43 +03:00 agreed 80 ms; both local and forwarded streams were ready, with no receiver drops observed at that check. The physical screenshot at 02:23:07 showed approximately **0.32 seconds locally and 0.36 seconds after forwarding**. This single reading is not a maximum-delay guarantee. [Measurements](docs/browser-and-camera-delay.md).

New sources and camera-02 keep the **300 ms** starting allowance. The 80 ms setting leaves less time to recover missing data on poor networks. [80/120 ms network comparison](reports/network-delay-80ms-comparison.json). Follow [the source guide](docs/source-setup.md#restore-the-camera-to-120-ms) to restore camera-01 to 120 ms.

The earlier controlled clean test saved approximately 180 ms per changed SRT allowance with every scored picture intact. The harder delay-and-loss test damaged or lost pictures at 120 ms. Those results compare 300 and 120 ms. [Earlier comparison results](reports/network-delay-comparison.md).

`relay-link local` selects authenticated RTSP over TCP between the two programs on this Mac. It keeps the picture settings and removes the SRT recovery allowance from that local connection. Its destination is fixed to this computer; it is not a mode for forwarding across an untrusted network. `relay-link srt` restores SRT with the saved recovery allowance. Changes affect only the selected forwarder. Earlier physical clock readings, with the camera at 120 ms, showed about 0.34–0.38 seconds locally and 0.36–0.38 seconds after forwarding. These are approximate observations, not guaranteed maximum delays. [Further optimization and measurements](docs/further-delay-optimization.md).

## Recording

Recordings are saved under `recordings/` in source-labelled session folders, using roughly five-second MP4 pieces. SQLite in `.local/` records each completed file's source, checksum and upload progress. A checksum helps detect whether file contents changed.

Each source's recorder runs separately from its forwarder. The recording budget is **2 GB shared across all sources**. All recorders pause when the budget is reached or less than **1 GB** of disk space is free. The lab never automatically deletes footage. These limits are checked every few seconds, so they are protective thresholds rather than exact ceilings. Move saved footage outside `recordings/` to release its budget. Use `./lab status` for the pause reason.

An incomplete file may remain after a crash. Only finished files listed in `segments.csv` are entered into the completed-file database. The controller flushes completed files before recording their metadata. Physical power-loss recovery has not been validated, and the program does not encrypt local recordings itself. Protect the Mac and its disk.

Testing also found that an unexpected source disconnect can leave an incomplete compressed frame inside a finalized MP4. A checksum and an R2 confirmation prove that the saved bytes were copied; they do not prove that every frame can be decoded. The lab preserves that footage without repairing it, and does not yet display a separate recording-quality flag. Orderly shutdown stops the recorder before stopping a generated sender; it is a different case from losing a camera connection.

## Optional R2 archive

R2 is disabled in a fresh installation and is enabled in this configured workspace. Run `./lab archive-config` to create or locate the private configuration, then follow [R2 setup](docs/r2-setup.md). The uploader processes one recording at a time, retains source identity, resumes pending work after restart and confirms uploads before marking them archived. It does not delete local files.

Its default fixed limit is 1 megabit per second. A continuously recorded 2-megabit camera will create a backlog; multiple cameras increase that rate. The limit does not automatically adapt to live-video needs. The successful R2 check verifies the basic upload and confirmation path, not every interruption or recovery case.

## Test without camera hardware

```sh
./lab start
./lab --source camera-01 demo
./lab --source camera-02 demo
./lab stop
./lab test
```

Register `camera-02` before using its demo commands. Each demo is a moving **test pattern**. Use `./lab stop` to finish the whole demonstration: it stops recorders before generated senders. To replace only one demo with a real device, use `./lab --source camera-01 demo-stop`; this removes that source and can leave an incomplete final frame. Other sources can keep sending. The acceptance test checks simultaneous decoding and recording, separate forwarding controls, both recovery waits, the smaller picture setting, rejected credentials and SQLite persistence. It requires the normal lab to be stopped and uses private, loopback-only settings and recordings. Reports go in `reports/`; the selected experimental receiver passed all **85 assertions**, including a frozen receiver, both forwarding links, full decoding of **44 finalized clips** and SQLite persistence after orderly shutdown. [Experimental receiver acceptance results](reports/integration-lan80-receiver.json).

These tests do not measure camera-to-screen delay or establish long-term availability. The single-source generated browser check is recorded separately from media-file and two-source checks.

## Stop and diagnose

Double-click **Stop Video Lab.command**, or run `./lab stop`. Recordings are kept. The lab does not start automatically when the Mac boots.

`./lab logs` shows recent logs with generated connection secrets hidden. Raw logs, settings and source guides are private files in `.local/`; do not share that folder. `./lab status` distinguishes a failed status check from a confirmed connection state, but does not prove that every displayed frame is recent.

If the controller disappears while video services remain, commands report those services rather than claim they stopped. Automatic cleanup after a forced controller termination remains limited; do not kill unrelated processes to free ports.

## Installation and boundaries

Building from source requires Go 1.25 or newer. Running the built controller does not require Go or Python. Video processing requires FFmpeg and FFprobe with H.264 and SRT support. Setup downloads **MediaMTX 1.21.0** from its official GitHub release and checks the pinned SHA-256 checksum. It installs inside this folder and does not modify system packages.

The physical-camera delay investigation reproduced a timing defect in that receiver's SRT library. A separate local correction is described in [the delay investigation](docs/delay-investigation.md). An explicit `.tools/mediamtx-active` directory link selects a locally built receiver; without it, the controller uses the official release. Setup continues to maintain the original release separately. A broken selection causes startup to fail instead of silently reverting.

This workspace currently selects `.tools/mediamtx-active -> mediamtx-v1.21.0-clockfix1-lan60`. This experimental build retains the clock correction and lowers the listener's minimum waiting allowance to 60 ms, allowing camera-01 to request 80 ms. The agreed allowance is the larger of the receiver minimum and the sender request, so existing 120 or 300 ms senders keep those allowances. New-source setup still recommends 300 ms. [Build and negotiation evidence](reports/experimental-receiver-lan60.json).

```sh
./lab build
./lab setup
./lab start
```

Each source requires its own generated publisher credentials and SRT media passphrase. Management, viewers and forwarding between local services are restricted to this computer. This milestone assumes a trusted computer and local network. Private tunnels for untrusted networks, stronger viewer access controls, separate machines, Linux network impairment and full browser freshness measurements remain future work.

The source tree contains Go code and an embedded text template for each source's private instructions. The built program does not need the source template on disk. The small `lab` shell script launches the compiled Go program; it builds only when explicitly asked with `./lab build`. The earlier Python implementation has been removed.

See [the design notes and DDIA connections](docs/design.md).
