import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {createHash} from 'node:crypto';

const asset = name => fs.readFileSync(new URL('./assets/' + name, import.meta.url), 'utf8');
const encoder = vm.createContext({TextEncoder});
vm.runInContext(asset('vendor/qrcode.js'), encoder);
const QRCode = encoder.QRCode;

function surface(width, height) {
  const pixels = new Uint8ClampedArray(width * height * 4);
  pixels.fill(255);
  return {width, height, pixels};
}
function drawCode(image, payload, x = 0, y = 0, scale = 6) {
  const qr = QRCode.create(payload, {errorCorrectionLevel: 'M'});
  const extent = (qr.modules.size + 8) * scale;
  assert.ok(x >= 0 && y >= 0 && x + extent <= image.width && y + extent <= image.height);
  for (let row = 0; row < qr.modules.size; row++) {
    for (let column = 0; column < qr.modules.size; column++) {
      if (!qr.modules.get(row, column)) continue;
      for (let dy = 0; dy < scale; dy++) {
        for (let dx = 0; dx < scale; dx++) {
          const offset = ((y + (row + 4) * scale + dy) * image.width + x + (column + 4) * scale + dx) * 4;
          image.pixels[offset] = image.pixels[offset + 1] = image.pixels[offset + 2] = 0;
        }
      }
    }
  }
  return extent;
}
function oneCode(payload, scale = 6) {
  const size = (QRCode.create(payload, {errorCorrectionLevel: 'M'}).modules.size + 8) * scale;
  const image = surface(size, size);
  drawCode(image, payload, 0, 0, scale);
  return image;
}
function rotated(image) {
  const out = surface(image.height, image.width);
  for (let y = 0; y < image.height; y++) {
    for (let x = 0; x < image.width; x++) {
      const from = (y * image.width + x) * 4;
      const to = (x * out.width + image.height - y - 1) * 4;
      out.pixels.set(image.pixels.subarray(from, from + 4), to);
    }
  }
  return out;
}
function worker() {
  const replies = [];
  let calls = 0;
  const sandbox = vm.createContext({ArrayBuffer, Uint8ClampedArray, TextEncoder});
  sandbox.self = sandbox;
  sandbox.postMessage = message => replies.push(JSON.parse(JSON.stringify(message)));
  sandbox.importScripts = path => {
    assert.equal(path, '/vendor/jsQR.js', 'worker imports only its fixed local decoder');
    vm.runInContext(asset('vendor/jsQR.js'), sandbox);
    const decode = sandbox.jsQR;
    sandbox.jsQR = (...args) => { calls++; return decode(...args); };
  };
  vm.runInContext(asset('qr-worker.js'), sandbox);
  return {
    send(value) {
      const before = replies.length, scans = calls;
      sandbox.onmessage({data: value});
      assert.equal(replies.length, before + 1, 'exactly one response per request');
      assert.ok(calls - scans <= 2, 'no more than two scans');
      return {reply: replies.at(-1), scans: calls - scans};
    },
    image(image, id = 7) {
      return this.send({id, width: image.width, height: image.height, buffer: image.pixels.buffer.slice(0)});
    },
  };
}
const payload = 'FIELDSTREAM:1:0123456789abcdef0123456789abcdef:12345';

test('pinned encoder produces deterministic modules without a DOM or network', () => {
  const first = QRCode.create(payload, {errorCorrectionLevel: 'M'});
  const second = QRCode.create(payload, {errorCorrectionLevel: 'M'});
  assert.deepEqual([...first.modules.data], [...second.modules.data]);
  assert.ok(first.modules.size >= 21);
  assert.deepEqual(Object.keys(QRCode), ['create']);
});

test('real encoded QR is decoded uniquely with a bounded second scan', () => {
  const result = worker().image(oneCode(payload));
  assert.deepEqual(result.reply, {id: 7, status: 'read', payload});
  assert.equal(result.scans, 2);
});

test('real QR decode tolerates 90, 180 and 270 degree rotation', () => {
  const decoder = worker();
  let image = oneCode(payload);
  for (let i = 0; i < 3; i++) {
    image = rotated(image);
    assert.deepEqual(decoder.image(image, i).reply, {id: i, status: 'read', payload});
  }
});

test('blank and severely damaged images remain unreadable', () => {
  const decoder = worker();
  assert.deepEqual(decoder.image(surface(640, 360)).reply, {id: 7, status: 'unreadable'});
  const image = oneCode(payload);
  // Remove two of the three finder corners and most data; no invented payload.
  for (let y = 0; y < image.height; y++) {
    image.pixels.fill(255, y * image.width * 4, (y * image.width + Math.floor(image.width * 0.72)) * 4);
  }
  assert.deepEqual(decoder.image(image).reply, {id: 7, status: 'unreadable'});
});

test('two readable QR codes are ambiguous even with the same payload', () => {
  const decoder = worker();
  for (const second of [payload, 'FIELDSTREAM:1:0123456789abcdef0123456789abcdef:12344']) {
    const image = surface(960, 540);
    drawCode(image, payload, 20, 80);
    drawCode(image, second, 520, 260, 4);
    const result = decoder.image(image);
    assert.deepEqual(result.reply, {id: 7, status: 'ambiguous'});
    assert.equal(result.scans, 2);
  }
});

test('oversized decoded text is never returned', () => {
  assert.deepEqual(worker().image(oneCode('X'.repeat(129), 4)).reply, {id: 7, status: 'unreadable'});
});

test('invalid dimensions, IDs and buffers are rejected before decoding', () => {
  const decoder = worker();
  const valid = {id: 4, width: 1, height: 1, buffer: new ArrayBuffer(4)};
  for (const change of [
    {width: 0}, {height: -1}, {width: 961}, {height: 541}, {width: 2.5}, {height: Infinity},
    {buffer: new ArrayBuffer(3)}, {buffer: new Uint8ClampedArray(4)}, {buffer: null},
    {id: -1}, {id: Number.MAX_SAFE_INTEGER + 1}, {id: 'untrusted-' + 'x'.repeat(1000)},
  ]) {
    const result = decoder.send({...valid, ...change});
    assert.equal(result.reply.status, 'unreadable');
    assert.equal(result.scans, 0);
    assert.ok(result.reply.id === null || result.reply.id === 4);
  }
  assert.deepEqual(decoder.send(null).reply, {id: null, status: 'unreadable'});
});

test('vendored assets match the committed manifest and include licenses', () => {
  const manifest = JSON.parse(asset('vendor/manifest.json'));
  assert.equal(manifest.packages.qrcode.version, '1.5.4');
  assert.equal(manifest.packages.jsqr.version, '1.4.0');
  assert.equal(manifest.packages.dijkstrajs.version, '1.0.3');
  for (const [name, expected] of Object.entries(manifest.assetSHA256)) {
    const digest = createHash('sha256').update(fs.readFileSync(new URL('./assets/vendor/' + name, import.meta.url))).digest('hex');
    assert.equal(digest, expected, name);
  }
  for (const name of ['qrcode-LICENSE.txt', 'dijkstrajs-LICENSE.txt']) {
    assert.match(asset('vendor/' + name), /Permission is hereby granted|Licensed under the MIT license/);
  }
  assert.match(asset('vendor/jsqr-LICENSE.txt'), /Apache License\s+Version 2\.0/);
});
