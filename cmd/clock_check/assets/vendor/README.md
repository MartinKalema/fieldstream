# Pinned QR browser code

The clock page encoder exposes `globalThis.QRCode.create(text, options)`.
For example:

```js
const qr = QRCode.create(payload, {errorCorrectionLevel: 'M'});
const widthInModules = qr.modules.size;
const black = qr.modules.get(row, column);
```

Draw the matrix locally with a white quiet border of at least four modules.
This encoder build deliberately exports only `create`; it does not provide
canvas, network, file or URL-opening helpers.

`qrcode.js` contains the unchanged algorithm modules from locked qrcode 1.5.4
and dijkstrajs 1.0.3, wrapped with a deterministic local CommonJS loader.
`jsQR.js` is the unchanged jsQR 1.4.0 browser distribution. The encoder's MIT
licenses and decoder's Apache 2.0 license are included beside the files.
`manifest.json` records package lock integrity
and source/output SHA-256 hashes.

Regenerate from the repository root without adding any build dependency:

```sh
npm ci --ignore-scripts --prefix scripts/qr-tools
node scripts/qr-tools/vendor-clock-qr.cjs
node scripts/qr-tools/vendor-clock-qr.cjs --check
```

The decoder worker loads only the local `/vendor/jsQR.js` file. Requests have
`{id, width, height, buffer}` where `id` is a nonnegative safe integer and
`buffer` is a transferred RGBA ArrayBuffer, with exact byte length. Width is
at most 960 and height at most 540. Responses are `{id, status:'unreadable'}`,
`{id, status:'ambiguous'}`, or `{id, status:'read', payload}`. A read payload
has at most 128 JavaScript characters. Invalid IDs return `id:null`.

Each valid request makes at most two full decoder calls: read one symbol,
mask its bounding rectangle, and look once more for a second readable symbol.
This rejects a detected second code, including a duplicate payload. It cannot
prove there are no damaged, occluded or overlapping codes in the picture.
Unreadable content never becomes an estimated age. The page must validate the
session payload, keep one request in flight and terminate a decoder exceeding
its time budget. No decoded string is followed as a link or executed.
