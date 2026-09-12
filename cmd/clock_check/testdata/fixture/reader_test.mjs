import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const script = readFileSync(new URL('./reader.js', import.meta.url), 'utf8');

function fixture(route = 'forwarded') {
  let now = 0, requested = 0;
  const qrCalls = [];
  class Canvas {
    constructor() {
      this.width = this.height = 296;
      this.content = null;
      this.draws = [];
      this.hidden = false;
      this.isConnected = true;
      this.context = {
        fillRect: () => {}, fillText: () => {},
        drawImage: (source, ...coordinates) => {
          this.content = source.content;
          this.draws.push({content: source.content, coordinates});
        },
      };
    }
    getContext() { return this.context; }
    getClientRects() { return this.hidden ? [] : [{}]; }
  }
  const marker = new Canvas();
  marker.content = 'marker-0';
  const context = vm.createContext({
    document: {
      readyState: 'loading',
      getElementById: id => id === 'delay-marker' ? marker : null,
      createElement: tag => { assert.equal(tag, 'canvas'); return new Canvas(); },
      addEventListener() {},
    },
    performance: {now: () => now},
    getComputedStyle: () => ({visibility: 'visible'}),
    addEventListener() {}, queueMicrotask() {}, clearInterval() {},
    QRCode: {create(data, options) {
      qrCalls.push({data, level: options.errorCorrectionLevel});
      return {modules: {size: 29, get: () => false}};
    }},
  });
  vm.runInContext(script, context, {filename: 'reader.js'});
  const source = new context.MediaMTXWebRTCReader({});
  source.route = route;
  source.canvas = new Canvas();
  source.context = source.canvas.context;
  source.track = {requestFrame: () => requested++};
  return {source, marker, qrCalls, setTime: value => { now = value; }, requests: () => requested};
}

test('forwarded marker history uses at most thirty reusable small canvases', () => {
  const {source, marker} = fixture();
  for (let i = 0; i < 30; i++) { marker.content = i; source.opticalImage(i * 50); }
  const allocated = new Set(source.markerHistory.map(item => item.canvas));
  for (let i = 30; i < 300; i++) { marker.content = i; source.opticalImage(i * 50); }
  assert.equal(source.markerHistory.length, 30);
  assert.ok(source.markerHistory.every(item => allocated.has(item.canvas) && item.canvas.width === 296 && item.canvas.height === 296));
  assert.equal(source.opticalImage(15000).content, 299);
  source.close();
  assert.equal(source.markerHistory.length, 0);
  assert.equal(source.frozenMarker, null);
});

test('delay uses elapsed time and copies pixels rather than retaining the changing canvas', () => {
  const {source, marker} = fixture();
  source.opticalMode = 'delay';
  marker.content = 'old';
  assert.equal(source.opticalImage(0), null);
  marker.content = 'new';
  assert.equal(source.opticalImage(999), null);
  assert.equal(source.opticalImage(1000).content, 'old');
  assert.equal(source.opticalImage(1300).content, 'old');
  assert.equal(source.opticalImage(2100).content, 'new');
});

test('frozen optical marker does not freeze source frame requests', () => {
  const {source, marker, setTime, requests} = fixture();
  source.setOpticalMode('freeze');
  for (let i = 1; i <= 12; i++) {
    marker.content = 'marker-' + i;
    setTime(i * 50);
    source.draw();
    assert.equal(source.canvas.draws.at(-1).content, 'marker-0');
  }
  assert.equal(requests(), 13);
  source.setOpticalMode('normal');
  assert.equal(source.canvas.draws.at(-1).content, 'marker-12');
  assert.equal(source.frozenMarker, null);
});

test('local marker stays fresh and never queues forwarded delay or freeze', () => {
  const {source, marker} = fixture('local');
  source.setOpticalMode('delay');
  assert.equal(source.opticalMode, 'normal');
  assert.equal(source.opticalImage(0), marker);
  assert.equal(source.markerHistory.length, 0);
  marker.content = 'fresh';
  assert.equal(source.opticalImage(100).content, 'fresh');
});

test('hidden target clears old images and renders no QR even in wrong-session mode', () => {
  const {source, marker, qrCalls} = fixture();
  source.setOpticalMode('freeze');
  assert.ok(source.frozenMarker);
  marker.hidden = true;
  assert.equal(source.opticalImage(200), null);
  assert.equal(source.frozenMarker, null);
  assert.equal(source.markerHistory.length, 0);
  source.setOpticalMode('wrong-session');
  assert.equal(qrCalls.length, 0);
  marker.hidden = false;
  assert.ok(source.opticalImage(400));
  assert.deepEqual(qrCalls, [{data: 'FS1:000000000000000000000000:0', level: 'M'}]);
});

test('two-code mode draws two copies while blank mode draws none; hold controls still stop frames', () => {
  const {source, requests} = fixture();
  source.setOpticalMode('two');
  assert.equal(source.canvas.draws.length, 2);
  assert.deepEqual(Array.from(source.canvas.draws[0].coordinates), [24, 72, 220, 220]);
  assert.deepEqual(Array.from(source.canvas.draws[1].coordinates), [336, 72, 220, 220]);
  source.canvas.draws.length = 0;
  source.setOpticalMode('blank');
  assert.equal(source.canvas.draws.length, 0);
  const before = requests();
  source.hold(true);
  source.draw();
  assert.equal(requests(), before);
  source.hold(false);
  assert.equal(requests(), before + 1);
});
