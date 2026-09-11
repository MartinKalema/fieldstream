# Connect video sources

A source is one separately identified video stream. Give each device its own source so its picture, recording and controls remain distinct. The first source is `camera-01`; up to four can be registered in this version.

A phone, tablet, compatible camera or another computer can send video. The current input requires **H.264 video in MPEG-TS over SRT**, with caller mode, a Stream ID and an encryption passphrase. A camera that only supports a different connection type needs an adapter before it can connect. The receiver does not depend on a particular device brand.

Both video services run on this Mac for the first experiment. A fresh installation leaves uploads disabled. This workspace has already enabled and checked [the R2 archive](r2-setup.md), so completed recordings will also be queued for upload when the lab starts.

## Prepare the receiving computer

The lab's control program is written in Go. To build it from source, the Mac needs the Go development tools described in the [project README](../README.md). Building means turning the source code into a program the computer can run. Once that program is built and installed, it runs without Go development tools. FFmpeg and MediaMTX remain separate video programs used by the lab.

Open Terminal and run:

```sh
cd ~/Desktop/field-video-lab
./lab build
./lab setup
./lab source list
./lab source guide camera-01
./lab start
```

Setup creates `.local/connections/camera-01.txt`. The `source guide` command prints that file's location without exposing its contents. Open it for the current network address and that source's connection values. `SOURCES.txt` lists all guide files and viewer addresses without listing passwords.

Keep connection guide files private: their credentials let a device send video into the assigned source.

## Add another device

Stop the lab before changing its source list. This workspace already has `camera-01` and `camera-02`; run `source list` before adding a new ID. For a fresh setup, the second-source example is:

```sh
./lab stop
./lab source add camera-02 --label "Second camera"
./lab source guide camera-02
./lab source list
./lab start
```

Configure the second device using `.local/connections/camera-02.txt`. Each device uses its own source ID and credentials. Both can send at once. A second device cannot replace another source's active publisher.

For any compatible sender, enter the connection URL, full Stream ID, caller mode, encryption passphrase, 128-bit key length and 300-millisecond latency from its guide. The URL alone is not sufficient. Use MPEG-TS as the container if the sender asks.

## Example: a phone or tablet using Larix

Install **Larix Broadcaster**. This is the camera-streaming app; Larix Player is a different app for watching video. Larix supports sending video from an iPad using H.264 and SRT. [Official Larix iOS information](https://softvelum.com/larix/ios/).

Connect the sender and receiving computer to the same trusted local network, or to the same hotspot. Guest Wi-Fi may block communication between devices even when they use the same network name.

Open Larix and allow camera and **Local Network** access when the device asks. If previously denied, check the device's privacy settings for Larix.

Keep Larix in the foreground and keep the device awake during this first test.

## Add the connection

Menu names can vary slightly between Larix versions. The connection and passphrase steps follow [Larix's SRT setup guide](https://blog.wmspanel.com/2018/03/srt-mobile-streaming-android-ios.html).

1. Open Larix settings using the gear button.
2. Open **Connections**, tap **+**, and choose the ordinary **Connection** option. WebRTC Connection and Zixi Connection are different methods.
3. Name it after the source label, such as **Second camera**.
4. Replace the URL field's RTMP example with the entire SRT URL from that source's private guide, including `srt://`. Do not append `/live/stream`. The ordinary connection form also supports SRT. Fill in the **Stream ID** and **SRT passphrase** from the same guide; if extra settings are not visible, save the connection and reopen its details.
5. If a mode is shown, choose **Caller** or **Push**. The sender starts the connection to the receiving computer.
6. Set the encryption key length to **128 bits** and initial latency to **300 milliseconds**, following the private guide.
7. Save the connection and select it as the connection to use.

**SRT** means Secure Reliable Transport. It is a way of sending live video that can request missing pieces again, within a limited waiting time. Here it also uses a passphrase to encrypt the video. It cannot recover video during a complete disconnection. [MediaMTX SRT documentation](https://mediamtx.org/docs/publish/srt-clients).

The Stream ID contains a username and password. Those control permission to publish. The encryption passphrase has a different job: it protects the video data during transfer.

## Choose a simple starting picture

If the ordinary URL form rejects the address, this workspace also has private
Larix Grove import codes in `.local/connections/camera-01-larix-qr.png` and
`camera-02-larix-qr.png`. Open the appropriate image on the Mac, scan it using
the device's normal Camera app, and open it in Larix to review and import the
connection. This fills the source address, Stream ID, encryption passphrase,
Caller mode and video-only selection. It does not change the video encoder
settings below. [Official Grove import instructions](https://softvelum.com/larix/grove/).

These codes contain source credentials and must stay private. They are snapshots
of the current connection settings; regenerate them if the computer's address or
source credentials change. QR contents have been decoded and checked locally,
and the first source sent physical-camera video after import. The native
form rejection observed on Larix 1.8.7 build 690 with iPadOS 26.5 has no confirmed
root cause yet.

Use these settings for the first test:

| Setting | Starting choice | What it means |
| --- | --- | --- |
| Video format / codec | H.264 / AVC | The method used to compress the picture |
| Resolution | 1280 × 720, also called 720p | The number of picture dots; a manageable starting size |
| Frame rate | 30 fps | Thirty pictures each second |
| Video bitrate | 2,000 kbps, or 2 Mbps | The target amount of video data sent each second |
| Container, if requested | MPEG-TS | Carries the compressed video over this connection |
| Keyframe interval, if available | 2 seconds | How often an independently decodable picture is sent |
| B-frames, if available | Off | Avoids a picture-ordering mode that can prevent this browser path from working |
| Audio | Video only | Keeps the first experiment focused on the picture |
| Automatic bitrate changes | Off initially | Makes the first comparison easier to understand |

These are starting settings for this lab, not a claim that they suit every scene. We can later try 1920 × 1080 at 30 fps to compare detail and data use.

Return to the camera view and press Larix's start-streaming button. Point it at something with movement and a readable label.

## Watch on the Mac

Open these links in a browser **on the Mac**:

- [Local picture](http://127.0.0.1:18889/camera-01) — directly from the first video service.
- [Forwarded picture](http://127.0.0.1:28889/camera-01) — after the separate forwarding program.

Use `/camera-02` for the second source, or run `./lab source list` for every address. `127.0.0.1` means the computer running the browser. Opening these links on another device will not reach the receiving computer.

If the player asks you to press Play, do so. Allow a few seconds for the connection and picture to start.

Check the programs from Terminal:

```sh
./lab status
```

Program status does not prove that the picture is fresh. Watch for movement in both players.

Recordings are saved in the project's `recordings` folder, in session folders beginning with their source ID. SQLite tracks source identities, completed files, checksums and upload progress in `.local/recordings.sqlite`. A checksum helps detect changed contents. The 2 GB recording budget is shared across sources. R2 is a separate background step and is enabled in this workspace.

An unexpected sender disconnect can leave an incomplete last frame inside a saved MP4. The lab keeps those bytes; it does not repair the recording or mark individual damaged frames. A confirmed R2 copy is a copy of the saved bytes, not a guarantee that every picture plays correctly.

## Try the first failure experiment

With both sources sending, stop only one forwarding program. Put `--source` before the command:

```sh
./lab --source camera-01 relay-off
```

That source's local picture and recording should continue. The other source should also keep working. Only the selected forwarded picture stops receiving new video. Observe how its player behaves; this prototype does not measure the true age of each displayed frame.

Start forwarding again:

```sh
./lab --source camera-01 relay-on
```

Check that the forwarded picture returns to what the camera sees now. If the browser does not reconnect, reload its page and record that as a recovery limitation.

This experiment stops a program. It does not yet reproduce internet packet loss or prove recovery from a real network failure.

## Try reducing the forwarded video

Start with the source video forwarded without another round of compression:

```sh
./lab --source camera-02 profile copy
```

Then select the smaller delivery profile:

```sh
./lab --source camera-02 profile small
```

Compare readable text and movement in that source's two browser windows. Its local window shows what reached the receiving computer. Other sources keep their own settings. This is a visual check, not a measured quality score.

The smaller profile changes the video **after it reaches the receiving computer**. It cannot reduce data already sent from the source. Change the sender's bitrate or resolution to reduce that first connection's traffic.

Return to the source-copy profile when finished:

```sh
./lab --source camera-02 profile copy
```

## Compare forwarding delay

Each connection has a waiting allowance for recovering missing video data. A shorter allowance may reduce delay, but more pictures may be damaged when the connection is poor. The camera-to-Mac connection and the forwarding connection have separate allowances.

To change only one source's forwarding allowance while the lab is running:

```sh
./lab --source camera-01 relay-wait 120
./lab --source camera-01 status
```

The number is milliseconds. This briefly reconnects the selected forwarder. It leaves the incoming camera settings, recording, picture size and other sources unchanged. To restore the initial allowance:

```sh
./lab --source camera-01 relay-wait 300
```

The command accepts only 120 or 300. The chosen value is saved for the next lab start and is used when the SRT forwarding link is selected. Status shows the requested value; the actual allowance is agreed between the two connected programs. The lab's acceptance test checks that both values are agreed correctly with its selected receiver.

Changing the camera's own allowance requires a separate change in its sender app. First compare forwarding on its own, then repeat the filmed-clock measurement after any camera-side change. A shorter queue reported by a server does not by itself prove how old the picture in the browser is. See [the network experiment](network-delay-test.md).

On the observed Larix version, open **Connections**, then **Manage** at the bottom-right, then the connection name to find **latency (msec)**. Stop and restart broadcasting after a change. The private 120 ms and 300 ms restore QR imports are another way to apply that setting. A successful import does not change a connection that was already running; verify the newly connected receiver afterward.

When both video services are on this Mac, the separate `relay-link local` option uses a direct local connection without an SRT recovery allowance:

```sh
./lab --source camera-01 relay-link local
```

Restore SRT with `./lab --source camera-01 relay-link srt`. The local option is restricted to this computer, retains publisher authentication, and supports both picture profiles. It does not change the camera connection. See [the further optimization measurements](further-delay-optimization.md).

## Current camera-01 local network experiment

Camera-01 now uses an **80 ms camera-to-Mac allowance** on the current local network. Its connection established on 12 September 2026 at 02:14:43 +03:00 agreed 80 ms; the local and forwarded streams were ready and the receiver reported no drops at that check. Forwarding stays local with the copy profile, preserving the incoming compressed picture. The physical screenshot at 02:23:07 showed approximately **0.32 seconds locally and 0.36 seconds after forwarding**. This single reading is not a maximum-delay guarantee. [Measurements](browser-and-camera-delay.md).

The selected receiver is `.tools/mediamtx-active -> mediamtx-v1.21.0-clockfix1-lan60`. This separate experimental build keeps the clock correction and allows a minimum of 60 ms. Camera-01 requests 80 ms, so its agreed allowance is 80 ms. A sender requesting 120 or 300 ms still gets that longer allowance. The normal setup guide and new sources retain **300 ms**; camera-02 also keeps its existing 300 ms allowance.

A shorter allowance gives late or missing data less time to arrive. The controlled 80/120 ms comparison retained every scored picture on the clean connection, shorter-delay loss test and brief-outage test. The harder delay-and-loss test retained fewer intact pictures at 80 ms. This qualifies 80 ms only for the current local-network experiment. [Network comparison](../reports/network-delay-80ms-comparison.json).

The experimental receiver also passed all **85 generated-video acceptance checks**, and all **44 completed recordings decoded fully**. Those checks cover two sources, failure recovery, permissions and orderly recording shutdown; they do not measure the age of a physical camera picture on the screen. [Acceptance report](../reports/integration-lan80-receiver.json).

### Restore the camera to 120 ms

1. Stop broadcasting in Larix.
2. Open the private [camera-01 120 ms restore QR](../.local/connections/camera-01-larix-120ms-qr.png) on the Mac and scan it with the iPad's Camera app.
3. Open the code in Larix and accept the import. It replaces the connection named **Field Video camera-01** and keeps its credentials. It includes no video-encoder changes.
4. Select **Field Video camera-01** and restart broadcasting. Check the fresh receiver connection to confirm the agreed allowance is 120 ms.

On the observed app version, the manual alternative is **Connections → Manage → Field Video camera-01 → latency (msec)**. Set it to **120**, then stop and restart broadcasting. Changing this camera setting does not require changing the saved forwarding allowance or the local forwarding mode.

The separate private [80 ms experiment QR](../.local/connections/camera-01-larix-80ms-qr.png) remains available. Keep both codes private, because they contain source credentials. Applying an import while broadcasting does not update the already-running connection; stop and restart the broadcast after any change.

## If the picture does not arrive

| What you see | What to check |
| --- | --- |
| Sender cannot connect | The lab is started, devices share a reachable local network, and source permissions are allowed |
| It stopped working after moving networks | Run `./lab stop`, then `./lab setup`, then `./lab start`; update each sender from its private guide |
| An authentication or encryption error | The full Stream ID and passphrase match that source's generated guide |
| macOS asks about incoming connections | Allow the lab's MediaMTX program on your trusted local network |
| Sender connects, but no browser picture | Select H.264, turn off B-frames and audio, and check `./lab status` |
| Local picture works, forwarded picture does not | Run `./lab --source SOURCE_ID relay-on`; check status and reload that player |
| A generated test pattern appears | Run `./lab --source SOURCE_ID demo-stop` before connecting that source's real device |
| A second device cannot use the same source | Give it its own registered source and private guide |

Do not forward router ports or expose these services to the public internet for this local experiment.

To test without physical hardware, use `./lab --source camera-01 demo`. Stop it with `./lab --source camera-01 demo-stop` before returning to that source's real device. Other sources can keep sending. Controls without `--source` select the first configured source; `start` and `stop` affect the whole lab.

For an orderly finish, stop the lab while the source devices are still sending:

```sh
./lab stop
```

Then stop the senders. This gives the recorders a chance to finish before their input disappears; it is different from an unexpected camera disconnection.
