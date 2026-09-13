# Reusable local tools

These tools are included in the source repository. Generated credentials, QR images, recordings and machine settings stay in the ignored `.local/` directory. Commands below run from the project root.

## Filmed-clock comparison

```sh
go run ./cmd/clock_check --source camera-01
```

Open the printed local URL. Point the broadcasting camera at the large clock and compare it with the clock visible in the local and forwarded videos. A screenshot containing all three clocks gives an approximate manual delay reading; screen refresh, exposure and frame timing limit precision.

For the optional [automatic picture-delay test](automatic-picture-delay.md), press **Start 2-minute measurement** and film only the direct white clock and changing pattern. Keep the page visible. It samples each player at most twice per second and reports approximate marker-age intervals, with failed and skipped work reported separately. These intervals are not verified sensor capture times or guaranteed delay bounds; use the filmed clock to cross-check them.

The [validation notes](automatic-picture-delay.md#checks-and-current-validation) record generated-video browser checks and a two-minute physical-camera run. That run accepted 192/232 local and 200/231 forwarded readings, with median midpoints of 0.340 and 0.370 seconds. Nearby manual clock observations are included with their timing limits; they do not establish exact accuracy or maximum delay.

The page uses two native browser video players with separate pause controls, picture-progress warnings and **Reconnect this viewer** buttons. An open connection does not prove that pictures are advancing. The warning watches browser presentation timestamps and keeps browser-default buffering unchanged. It does not establish the age of the original camera picture. See [how the warning works and what it cannot detect](picture-stall-warning.md).

The page is embedded in the Go program, so a built executable needs no separate HTML file. Build a private local executable with:

```sh
go build -o .tools/bin/clock_check ./cmd/clock_check
```

The default page address is `127.0.0.1:19080`. Use `--listen 127.0.0.1:19082` if that port is already occupied. The tool accepts only loopback addresses. `--local-port` and `--forwarded-port` select fixed local viewer ports, defaulting to 18889 and 28889. By default, only the selected `--source` is allowed. To permit a second source explicitly:

```sh
go run ./cmd/clock_check --source camera-01 --sources camera-01,camera-02
```

Then `?source=camera-02` selects that allowed source in the page. Up to four distinct source IDs can be allowed; a query cannot select an unlisted source. Opening the page creates two readers for its selected source. **Reconnect this viewer** changes only that reader. Stop this separate tool with Ctrl+C; it does not start or stop the video lab or change recordings and uploads.

For the separate browser buffering experiment on port 19081, see [the browser_check guide](../cmd/browser_check/README.md).

## Live forwarding cost

```sh
go run ./cmd/relay_check --root . --source camera-01 --duration 30s
```

This reads the running forwarder's CPU, sampled memory and received payload rate. It saves a private report under `reports/relay-check-*` and changes no camera, service or upload settings. It rejects interrupted observations rather than averaging across a restart. It does not measure picture delay or freezes. Use it alongside the clock and browser checks; see [the command guide](../cmd/relay_check/README.md) and [live profile trial](live-detail-profile.md).

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
go test ./cmd/clock_check
node --test cmd/clock_check/watch_test.mjs
node --test cmd/clock_check/app_test.mjs
npm test --prefix scripts/qr-tools
```

These checks use HTTP test requests, controlled browser-observation inputs and temporary, fake source settings. They do not contact cameras, start live media services or change the normal workspace settings. The [isolated browser fixture](../cmd/clock_check/testdata/fixture/README.md) exercises the production picture warnings through real browser media APIs using generated video. The older copies inside `.local/diagnostics/` and `.tools/qr-tools/` are private working artifacts; repository users should use the versioned tools above.
