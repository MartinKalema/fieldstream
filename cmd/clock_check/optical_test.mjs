import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {createAge} from './assets/age.mjs';

// This runs the whole production integration with its real age engine. Only
// the ES-module import is adapted for the VM. Browser scheduling, video pixels,
// QR generation and worker decoding are controlled substitutes: these tests
// establish lifecycle boundaries, not optical accuracy or browser performance.
const production = readFileSync(new URL('./assets/optical.mjs', import.meta.url), 'utf8');
const importLine = "import {createAge} from './age.mjs';\n";
assert.ok(production.startsWith(importLine), 'update the VM adapter if production imports change');
const script = production.slice(importLine.length) +
  '\n;globalThis.testOptical = {snapshot: () => age.inspect(performance.now()), render};\n';

function harness({canvasAvailable = true, postFailure = false} = {}) {
  let now = 0, nextTimer = 1, nonce = 0, outstanding = 0, maximumOutstanding = 0;
  const timers = new Map(), animations = new Map(), elements = new Map(), workers = [], markers = [];

  class Element {
    constructor() { this.textContent = ''; this.hidden = false; this.disabled = false; this.dataset = {}; this.listeners = new Map(); }
    addEventListener(name, callback) {
      const callbacks = this.listeners.get(name) || [];
      callbacks.push(callback); this.listeners.set(name, callbacks);
    }
    dispatch(name, event = {}) { for (const callback of this.listeners.get(name) || []) callback(event); }
  }
  class Canvas extends Element {
    width = 400;
    height = 400;
    getContext() {
      return canvasAvailable ? {fillRect() {}, drawImage() {},
        getImageData: (_x, _y, width, height) => ({data: new Uint8ClampedArray(width * height * 4)})} : null;
    }
  }
  class Video extends Element {
    paused = false;
    ended = false;
    error = null;
    readyState = 2;
    videoWidth = 8;
    videoHeight = 8;
    callbacks = new Map();
    srcObject = {tracks: [{}], getVideoTracks() { return this.tracks; }};
    requestVideoFrameCallback(callback) { const id = nextTimer++; this.callbacks.set(id, callback); return id; }
    cancelVideoFrameCallback(id) { this.callbacks.delete(id); }
    frame() {
      for (const [id, callback] of [...this.callbacks]) {
        this.callbacks.delete(id);
        callback(now, {expectedDisplayTime: now});
      }
    }
  }
  class Worker {
    constructor(url) {
      assert.equal(url, '/qr-worker.js');
      this.jobs = []; this.terminated = false;
      workers.push(this);
    }
    postMessage(job, transfer) {
      assert.equal(this.terminated, false);
      if (postFailure) throw new Error('controlled pixel transfer failure');
      assert.equal(transfer.length, 1);
      assert.equal(transfer[0], job.buffer, 'pixel storage must be transferred instead of queued copies');
      this.jobs.push({job, outstanding: true});
      outstanding++;
      maximumOutstanding = Math.max(maximumOutstanding, outstanding);
    }
    complete(payload = markers[0], status = 'read') {
      const latest = this.jobs.at(-1);
      assert.ok(latest, 'a frame must be submitted before its reply');
      if (latest.outstanding) { latest.outstanding = false; outstanding--; }
      // Deliver even after terminate, representing an already-queued event.
      this.onmessage?.({data: {id: latest.job.id, payload, status}});
    }
    error() { this.onerror?.({message: 'controlled worker failure'}); }
    terminate() {
      this.terminated = true;
      for (const job of this.jobs) if (job.outstanding) { job.outstanding = false; outstanding--; }
    }
  }
  const document = new Element(), window = new Element();
  document.visibilityState = 'visible';
  document.getElementById = id => {
    if (!elements.has(id)) {
      const element = id === 'delay-marker' ? new Canvas() : id.endsWith('-video') ? new Video() : new Element();
      if (id === 'delay-marker') element.hidden = true;
      if (id.endsWith('-panel')) element.dataset.state = 'advancing';
      elements.set(id, element);
    }
    return elements.get(id);
  };
  document.createElement = tag => { assert.equal(tag, 'canvas'); return new Canvas(); };
  const context = vm.createContext({
    createAge, document, window, Worker, performance: {now: () => now},
    crypto: {getRandomValues: bytes => { bytes.fill(++nonce); return bytes; }},
    QRCode: {create: payload => { markers.push(payload); return {modules: {size: 1, get: () => true}}; }},
    setTimeout: (callback, delay) => { const id = nextTimer++; timers.set(id, {at: now + delay, callback}); return id; },
    clearTimeout: id => timers.delete(id),
    requestAnimationFrame: callback => { const id = nextTimer++; animations.set(id, {at: now + 16, callback}); return id; },
    cancelAnimationFrame: id => animations.delete(id),
  });
  vm.runInContext(script, context, {filename: 'production-optical.mjs'});
  return {
    workers, markers, elements,
    start() { document.getElementById('age-start').dispatch('click'); },
    stop() { document.getElementById('age-stop').dispatch('click'); },
    snapshot() { return context.testOptical.snapshot(); },
    render() {
      context.testOptical.render();
      return JSON.parse(document.getElementById('age-diagnostics').textContent);
    },
    frame(route = 'local') { document.getElementById(route + '-video').frame(); },
    replaceTrack(route = 'local') {
      const stream = document.getElementById(route + '-video').srcObject;
      stream.tracks = [{}];
      assert.equal(document.getElementById(route + '-video').srcObject, stream);
    },
    setVisible(visible) {
      document.visibilityState = visible ? 'visible' : 'hidden';
      document.dispatch('visibilitychange');
    },
    jumpTo(target) { assert.ok(target >= now); now = target; },
    advanceTo(target) {
      assert.ok(target >= now);
      for (let count = 0; ; count++) {
        assert.ok(count < 10000, 'unbounded production timer scheduling');
        const due = [...timers].map(([id, value]) => ({id, ...value, owner: timers}))
          .concat([...animations].map(([id, value]) => ({id, ...value, owner: animations})))
          .filter(event => event.at <= target).sort((a, b) => a.at - b.at || a.id - b.id)[0];
        if (!due) break;
        due.owner.delete(due.id);
        now = due.at;
        due.callback(now);
      }
      now = target;
    },
    animation() {
      const [id, callback] = [...animations][0];
      animations.delete(id); callback.callback(now);
    },
    lastTimeout() {
      const [id, callback] = [...timers].sort((a, b) => b[0] - a[0])[0];
      timers.delete(id); callback.callback();
    },
    maximumOutstanding: () => maximumOutstanding,
    close() { window.dispatch('pagehide'); },
  };
}

function successfulStart(t) {
  const h = harness();
  t.after(() => h.close());
  h.start();
  h.advanceTo(500);
  h.frame();
  h.workers.at(-1).complete();
  assert.equal(h.snapshot().lanes.local.summary.valid, 1);
  return h;
}

for (const entry of ['frame', 'reply', 'render']) {
  test(`replacing a track inside the same MediaStream stops calibration from ${entry}`, t => {
    const h = successfulStart(t);
    h.advanceTo(1000);
    h.frame();
    h.replaceTrack(entry === 'frame' ? 'forwarded' : 'local');
    if (entry === 'frame') h.frame('local');
    else if (entry === 'reply') h.workers.at(-1).complete();
    else h.render();
    const done = h.snapshot();
    assert.equal(done.active, false);
    assert.equal(done.lanes.local.current, null);
    assert.equal(done.lanes.local.summary.valid, 1, 'post-reconnect samples must not join the completed summary');
    assert.equal(h.workers.at(-1).terminated, true);
  });
}

for (const entry of ['frame', 'reply', 'render', 'animation', 'timeout']) {
  test(`the first ${entry} after a scheduling gap stops before accepting more data`, t => {
    const h = successfulStart(t);
    h.advanceTo(1000);
    h.frame();
    h.jumpTo(2001); // No callbacks ran during this simulated browser suspension.
    if (entry === 'frame') h.frame('forwarded');
    else if (entry === 'reply') h.workers.at(-1).complete();
    else if (entry === 'render') h.render();
    else if (entry === 'animation') h.animation();
    else h.lastTimeout();
    const done = h.snapshot();
    assert.equal(done.active, false);
    assert.equal(done.lanes.local.current, null);
    assert.equal(done.lanes.local.summary.valid, 1);
    assert.equal(done.lanes.local.summary.attempted, 1, 'the gap is not a normal failed decode attempt');
    assert.equal(h.workers.at(-1).terminated, true);
  });
}

test('one transferred pixel job stays bounded across timeout and worker replacement', t => {
  const h = harness();
  t.after(() => h.close());
  h.start();
  h.advanceTo(500);
  h.frame('local');
  const oldWorker = h.workers.at(-1);
  h.frame('forwarded');
  h.advanceTo(700);
  h.frame('forwarded');
  assert.equal(oldWorker.jobs.length, 1);
  assert.equal(h.workers.length, 1);
  h.advanceTo(750);
  assert.equal(oldWorker.terminated, true);
  assert.equal(h.snapshot().lanes.local.summary.attempted, 1);
  assert.equal(h.snapshot().lanes.local.current, null);
  h.frame('forwarded');
  const replacement = h.workers.at(-1);
  assert.notEqual(replacement, oldWorker);
  oldWorker.complete();
  oldWorker.error();
  assert.equal(h.snapshot().active, true, 'queued events from the terminated worker must not affect its replacement');
  assert.equal(h.snapshot().lanes.forwarded.summary.attempted, 0);
  replacement.complete();
  assert.equal(h.snapshot().lanes.forwarded.summary.valid, 1);
  assert.equal(h.maximumOutstanding(), 1);
});

test('a queued worker error cannot stop a newly started session', t => {
  const h = successfulStart(t);
  const oldWorker = h.workers.at(-1), oldSession = h.snapshot().session;
  h.stop();
  h.start();
  assert.notEqual(h.snapshot().session, oldSession);
  assert.equal(h.snapshot().lanes.local.summary.valid, 0);
  oldWorker.error();
  oldWorker.complete();
  assert.equal(h.snapshot().active, true);
  assert.equal(h.workers.at(-1).terminated, false);
  h.workers.at(-1).error();
  assert.equal(h.snapshot().active, false, 'a failure from the current worker must still stop the run');
});

test('hiding the page clears current and preserves only the finished summary', t => {
  const h = successfulStart(t);
  h.setVisible(false);
  assert.equal(h.snapshot().active, false);
  assert.equal(h.snapshot().lanes.local.current, null);
  assert.equal(h.snapshot().lanes.local.summary.valid, 1);
  assert.equal(h.workers.at(-1).terminated, true);
  h.setVisible(true);
  assert.equal(h.snapshot().active, false, 'visibility restoration cannot resume an old calibration');
  h.start();
  assert.equal(h.snapshot().active, true);
  assert.equal(h.snapshot().lanes.local.summary.valid, 0);
});

test('unsupported canvas remains a usable manual-clock fallback through page exit', () => {
  const h = harness({canvasAvailable: false});
  assert.doesNotThrow(() => { h.start(); h.render(); h.close(); });
  assert.equal(h.snapshot().active, false);
  assert.equal(h.workers.length, 0);
  assert.match(h.elements.get('age-notice').textContent, /unavailable.*manual clock/i);
});

test('stopping with a pending read keeps it in the displayed denominator', t => {
  const h = successfulStart(t);
  h.advanceTo(1000);
  h.frame();
  let picture = h.render().pictures.local;
  assert.equal(picture.startedReads, 2);
  assert.equal(picture.completedAttempts, 1);
  assert.equal(picture.inFlight, true);
  h.stop();
  picture = h.render().pictures.local;
  assert.equal(picture.startedReads, 2);
  assert.equal(picture.completedAttempts, 1);
  assert.equal(picture.inFlight, false);
  assert.match(h.elements.get('local-age-summary').textContent, /^Successful reads: 1\/2 /);
  h.start();
  picture = h.render().pictures.local;
  assert.equal(picture.startedReads, 0);
  assert.equal(picture.completedAttempts, 0);
});

test('failed worker posting is a started read even though no reply can complete', t => {
  const h = harness({postFailure: true});
  t.after(() => h.close());
  h.start();
  h.advanceTo(500);
  h.frame();
  const diagnostics = h.render(), picture = diagnostics.pictures.local;
  assert.equal(diagnostics.active, false);
  assert.equal(picture.startedReads, 1);
  assert.equal(picture.completedAttempts, 0);
  assert.equal(picture.inFlight, false);
  assert.match(h.elements.get('local-age-summary').textContent, /^Successful reads: 0\/1 /);
});
