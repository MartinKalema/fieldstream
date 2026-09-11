# Reusable local tools

These tools are included in the source repository. Generated credentials, QR images, recordings and machine settings stay in the ignored `.local/` directory. Commands below run from the project root.

## Filmed-clock comparison

```sh
go run ./cmd/clockcheck --source camera-01
```

Open the printed local URL. Point the broadcasting camera at the large clock and compare it with the clock visible in the local and forwarded videos. A screenshot containing all three clocks gives an approximate delay reading. This page does not calculate delay automatically; screen refresh, exposure and frame timing limit precision.

The page is embedded in the Go program, so a built executable needs no separate HTML file. Build a private local executable with:

```sh
go build -o .tools/bin/clockcheck ./cmd/clockcheck
```

The default page address is `127.0.0.1:19080`. Use `--listen 127.0.0.1:19082` if that port is already occupied. The tool accepts only loopback addresses. `--local-port` and `--forwarded-port` select viewer ports, defaulting to 18889 and 28889. `?source=camera-02` selects another source in the page. Stop this separate tool with Ctrl+C; it does not start or stop the video lab.

For the separate browser buffering experiment on port 19081, see [the browsercheck guide](../cmd/browsercheck/README.md).

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
go test ./cmd/clockcheck
npm test --prefix scripts/qr-tools
```

These checks use HTTP test requests and temporary, fake source settings. They do not contact cameras, start live media services or change the normal workspace settings. The older copies inside `.local/diagnostics/` and `.tools/qr-tools/` are private working artifacts; repository users should use the versioned tools above.
