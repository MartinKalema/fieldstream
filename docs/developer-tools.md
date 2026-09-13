# Reusable local tools

These tools are included in the source repository. Credentials and machine settings stay in `.local/`; recordings and private reports stay in their own ignored directories. Commands below run from the project root.

## Filmed-clock comparison

```sh
go run ./cmd/clock_check
```

Open the printed local URL and keep it visible. This page displays only a large white elapsed clock. It opens no video connection and performs no automatic measurement. Open the camera separately with `./lab --source camera-01 view local`, then point the broadcasting camera at the white clock.

Place the GStreamer window beside the clock without covering it. In one screenshot, subtract the clock filmed inside GStreamer from the direct clock. For example, 40.00 − 39.70 means approximately 0.30 seconds of picture delay. Repeat several times and record unreadable pictures and pauses too. Screen refresh, exposure, frame timing and screenshot capture limit precision; this does not establish a maximum delay.

The page is embedded in the Go program, so a built executable needs no separate HTML file. Build a private local executable with:

```sh
go build -o .tools/bin/clock_check ./cmd/clock_check
```

The default page address is `127.0.0.1:19080`. Use `--listen 127.0.0.1:19082` if that port is already occupied. The tool accepts only loopback addresses; `--listen` is its only setting. Select the camera and route in the GStreamer command, not the clock page. Stop the clock server with Ctrl+C; this does not stop the video lab or change recordings and uploads.

The old browser players, buffer experiment, playback warnings and automatic pattern reader have been removed. Their [warning findings](picture-stall-warning.md) and [automatic measurement results](automatic-picture-delay.md) remain historical notes. Those features have not been added to GStreamer.

## Desktop viewer comparison

For normal viewing, use `./lab view`, or `./lab --source camera-02 view forwarded`.
This opens GStreamer with a 50 ms receiver wait and stays open until closed.
Use `--latency-ms 100` if movement causes broken blocks. It is a trusted local
viewer; a user interface and server-enforced user roles remain future work.

For a separate, time-limited comparison of a registered source:

```sh
go run ./cmd/gstreamer_check --root . --source camera-01 --duration 120s
```

The diagnostic keeps a **100 ms reference default**; add `--latency-ms 50` to compare the normal viewer's setting. Use `--route forwarded` for the forwarded picture or `--gst-launch /path/to/gst-launch-1.0` for another installation. Both commands automatically find this checkout's private runtime when installed. They read the existing loopback video endpoint and change no camera, recording or upload settings. Their private reports record the process outcome, not camera-to-screen delay. See [the GStreamer guide](gstreamer-viewer.md) for installation details, decoder choices and the filmed-clock method.

## Live forwarding cost

```sh
go run ./cmd/relay_check --root . --source camera-01 --duration 30s
```

This reads the running forwarder's CPU, sampled memory and received payload rate. It saves a private report under `reports/relay-check-*` and changes no camera, service or upload settings. It rejects interrupted observations rather than averaging across a restart. It does not measure picture delay or freezes. Use it alongside filmed-clock readings from GStreamer; see [the command guide](../cmd/relay_check/README.md) and [live profile trial](live-detail-profile.md).

## Saved-video compression comparison

The [saved-video comparison tool](compression-comparison.md) remains available. Its browser page compares completed MP4 files for size, detail and processing cost. It does not receive live video and does not depend on the removed browser players.

## Private Larix QR codes

The optional QR tools need Node.js 18 or newer and npm. These are development tools, not dependencies of the Go controller or an already configured camera.

```sh
npm ci --ignore-scripts --prefix scripts/qr-tools
node scripts/qr-tools/generate.cjs
```

Run `./lab setup` first. The generator reads `.local/settings.json` and creates `.local/connections/SOURCE_ID-larix-qr.png` plus its `.txt` import link. It handles every registered source, or just one with `--source camera-01`. General imports retain the 300 ms starting allowance and do not replace an existing Larix connection by name.

To prepare a separate same-name connection update:

```sh
node scripts/qr-tools/generate-wait.cjs camera-01 120
```

This accepts 80, 120 or 300 ms, writes separately named files and sets Larix's same-name replacement option. The 300 ms filename includes `-restore`. The 80 ms option is for the qualified local-network experiment; it requires the separately tested receiver and provides less recovery time. Creating a QR code does not change a running connection.

All imports preserve video-only mode, source identity and credentials, and contain no encoder-setting fields. Every generated PNG is decoded locally and checked against its intended import values before being saved. Files are private (`0600`), and credentials are omitted from success and error messages. Keep the QR images and `.txt` links private: both contain credentials.

Both commands accept `--root /path/to/field-video-lab`; the default is the current working directory. Existing output files cause an error. Add `--force` only when deliberately regenerating them, such as after the receiving computer's address changes. This flag replaces files on disk; the connection replacement rule remains specific to each generator.

To apply a wait update, stop broadcasting, scan the code with the device's Camera app, accept the Larix import, select the named connection and restart broadcasting. Confirm the new connection's negotiated allowance. The [source setup guide](source-setup.md) includes camera-01's 120 ms restore steps.

## Check the tools

```sh
go test ./...
go vet ./...
go test -race ./...
node --test cmd/quality_check/page_test.mjs
npm test --prefix scripts/qr-tools
```

The ordinary checks use local test data and controlled processes; they do not validate the physical camera or a native display. Tests requiring extra media tools or explicit diagnostic environment settings can be skipped by their own prerequisites. The separate `./lab test` media acceptance run uses private test files but the normal lab's ports, so it requires the lab to be stopped. It does not test native picture presentation.

Use repeated physical-camera readings, movement, interruptions and longer viewing runs as separate checks. A successful process run is not proof that every picture was recent.
