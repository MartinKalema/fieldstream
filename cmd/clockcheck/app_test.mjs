import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {createWatch} from './assets/watch.mjs';

// Execute the entire production application with its actual watch module. Only
// the module import is replaced for this VM; individual functions are neither
// copied nor extracted. The fake browser controls scheduling and asynchronous
// receiver replies, so an indefinitely pending read needs no real-time sleep.
// This verifies application lifecycle, not browser WebRTC, decoding or display.
const production = readFileSync(new URL('./assets/app.mjs', import.meta.url), 'utf8');
const importLine = "import {createWatch} from './watch.mjs';\n";
assert.ok(production.startsWith(importLine), 'update the VM import adapter if production imports change');
const script = production.slice(importLine.length) + '\n;globalThis.testApp = {render};\n';
const flushPromises = () => new Promise(resolve => setImmediate(resolve));

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return {promise, resolve, reject};
}

function report(timestamp, bytesReceived, framesDecoded, id = 'video') {
  return new Map([[id, {id, timestamp, bytesReceived, framesDecoded, type: 'inbound-rtp', kind: 'video'}]]);
}

function browserHarness(forwardedReply) {
  let now = 0, nextTimer = 1, nextFrame = 1;
  const timers = new Map(), frames = new Map(), elements = new Map(), readers = [];
  const requests = {local: [], forwarded: []};
  const inFlight = {local: 0, forwarded: 0}, maximumInFlight = {local: 0, forwarded: 0};

  class Element {
    constructor() {
      this.textContent = '';
      this.dataset = {};
      this.disabled = false;
      this.listeners = new Map();
    }
    addEventListener(name, callback) {
      const callbacks = this.listeners.get(name) || [];
      callbacks.push(callback);
      this.listeners.set(name, callbacks);
    }
    dispatch(name, event = {}) {
      for (const callback of this.listeners.get(name) || []) callback(event);
    }
    replaceChildren(...children) { this.children = children; }
  }
  class Video extends Element {
    paused = true;
    ended = false;
    error = null;
    videoWidth = 640;
    videoHeight = 360;
    requestVideoFrameCallback(callback) {
      const id = nextFrame++;
      frames.set(id, callback);
      return id;
    }
    cancelVideoFrameCallback(id) { frames.delete(id); }
    play() { this.paused = false; return Promise.resolve(); }
  }
  class Track extends Element {
    kind = 'video';
    readyState = 'live';
    stop() { this.readyState = 'ended'; }
  }
  class Stream {
    tracks = [];
    getTracks() { return [...this.tracks]; }
    addTrack(track) { this.tracks.push(track); }
    removeTrack(track) { this.tracks = this.tracks.filter(item => item !== track); }
  }
  class Reader {
    constructor(config) {
      this.config = config;
      this.route = new URL(config.url).pathname.split('/')[2];
      this.incarnation = readers.filter(reader => reader.route === this.route).length;
      this.closed = false;
      const track = new Track();
      this.receiver = {
        track,
        transport: {state: 'connected'},
        getStats: () => {
          const request = {route: this.route, incarnation: this.incarnation, now};
          requests[this.route].push(request);
          inFlight[this.route]++;
          maximumInFlight[this.route] = Math.max(maximumInFlight[this.route], inFlight[this.route]);
          let reply;
          try {
            reply = this.route === 'forwarded' ? forwardedReply(request, requests.forwarded.length) :
              report(now, 1000 + now, 10 + Math.floor(now / 50), 'local');
          } catch (error) {
            reply = Promise.reject(error);
          }
          return Promise.resolve(reply).finally(() => { inFlight[this.route]--; });
        },
      };
      readers.push(this);
      queueMicrotask(() => {
        if (!this.closed) config.onTrack({track, receiver: this.receiver});
      });
    }
    close() {
      this.closed = true;
      this.receiver.transport.state = 'closed';
      this.receiver.track.stop();
    }
  }
  const document = new Element();
  document.readyState = 'loading';
  document.visibilityState = 'visible';
  document.getElementById = id => {
    if (!elements.has(id)) elements.set(id, id.endsWith('-video') ? new Video() : new Element());
    return elements.get(id);
  };
  document.querySelector = selector => {
    assert.equal(selector, 'main');
    return {dataset: {source: 'camera-01'}};
  };
  document.createElement = () => new Element();
  const window = new Element();
  const context = vm.createContext({
    createWatch, document, window, URL,
    location: {href: 'http://127.0.0.1:19080/'},
    performance: {now: () => now},
    MediaStream: Stream, MediaMTXWebRTCReader: Reader,
    requestAnimationFrame: callback => { const id = nextFrame++; frames.set(id, callback); return id; },
    cancelAnimationFrame: id => frames.delete(id),
    setTimeout: (callback, delay) => {
      const id = nextTimer++;
      timers.set(id, {at: now + delay, callback});
      return id;
    },
    clearTimeout: id => timers.delete(id),
  });
  vm.runInContext(script, context, {filename: 'production-clock-app.mjs'});

  return {
    requests, readers, inFlight, maximumInFlight,
    async start() {
      window.dispatch('DOMContentLoaded');
      await flushPromises();
    },
    async advanceTo(target) {
      assert.ok(target >= now, 'test clock must remain monotonic');
      for (let count = 0; ; count++) {
        assert.ok(count < 10000, 'production scheduled too many timers in one test interval');
        const due = [...timers].filter(([, timer]) => timer.at <= target)
          .sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
        if (!due) break;
        const [id, timer] = due;
        timers.delete(id);
        now = timer.at;
        timer.callback();
        await flushPromises();
      }
      now = target;
      await flushPromises();
    },
    async reconnect() {
      const button = document.getElementById('forwarded-reconnect');
      assert.equal(button.disabled, false, 'wait until the actual Reconnect control becomes available');
      button.dispatch('click');
      await flushPromises();
    },
    render() {
      context.testApp.render(now);
      return JSON.parse(document.getElementById('diagnostics').textContent);
    },
    async close() {
      window.dispatch('pagehide');
      await flushPromises();
    },
  };
}

for (const settlement of ['resolve', 'reject']) {
  test(`pending statistics stay bounded across repeated reconnects and resume after old ${settlement}`, async t => {
    const oldRead = deferred(), newRead = deferred();
    const harness = browserHarness(request => request.incarnation === 0 ? oldRead.promise : newRead.promise);
    t.after(() => harness.close());
    await harness.start();
    await harness.advanceTo(250);
    assert.equal(harness.requests.forwarded.length, 1);
    assert.equal(harness.inFlight.forwarded, 1);

    // The 800 ms timeout removes stale evidence but cannot cancel the browser
    // promise. Keeping its slot occupied prevents an unbounded request queue.
    await harness.advanceTo(1100);
    assert.equal(harness.render().pictures.forwarded.counters, null);
    assert.equal(harness.requests.forwarded.length, 1);
    assert.equal(harness.inFlight.forwarded, 1);

    for (const time of [1750, 3500, 5250]) {
      await harness.advanceTo(time);
      await harness.reconnect();
      await harness.advanceTo(time + 250);
      assert.equal(harness.requests.forwarded.length, 1, 'reconnect must not launch another unresolved read');
      assert.equal(harness.render().pictures.forwarded.counters, null);
    }
    assert.equal(harness.readers.filter(reader => reader.route === 'forwarded').length, 4);
    assert.equal(harness.maximumInFlight.forwarded, 1);

    if (settlement === 'resolve') oldRead.resolve(report(9000, 999999, 9999, 'old-receiver'));
    else oldRead.reject(new Error('old receiver closed'));
    await flushPromises();
    // Render before the new read resolves: neither the old result nor its
    // rejection may supply or alter the replacement receiver's counters.
    assert.equal(harness.render().pictures.forwarded.counters, null);
    await flushPromises();
    assert.equal(harness.requests.forwarded.length, 2, 'settling the old request must release its slot');
    assert.equal(harness.requests.forwarded[1].incarnation, 3, 'the next read must belong to the current viewer');
    assert.equal(harness.inFlight.forwarded, 1);

    newRead.resolve(report(10000, 45000, 450, 'new-receiver'));
    await flushPromises();
    assert.deepEqual(harness.render().pictures.forwarded.counters, {framesDecoded: 450, bytesReceived: 45000});
    assert.equal(harness.maximumInFlight.forwarded, 1);
  });
}

test('a timed-out read keeps its slot and its late result cannot replace current unavailable evidence', async t => {
  const delayed = deferred();
  const harness = browserHarness((request, call) => call === 2 ? delayed.promise :
    report(request.now, call * 1000, call * 10, 'current-receiver'));
  t.after(() => harness.close());
  await harness.start();
  await harness.advanceTo(500);
  assert.deepEqual(harness.render().pictures.forwarded.counters, {framesDecoded: 10, bytesReceived: 1000});
  await harness.advanceTo(750);
  assert.equal(harness.requests.forwarded.length, 2);
  await harness.advanceTo(1600);
  assert.equal(harness.render().pictures.forwarded.counters, null);
  await harness.advanceTo(2500);
  assert.equal(harness.requests.forwarded.length, 2, 'timeout must not free a request that the browser has not settled');
  assert.equal(harness.inFlight.forwarded, 1);

  delayed.resolve(report(99999, 999999, 9999, 'late-result'));
  await flushPromises();
  assert.equal(harness.render().pictures.forwarded.counters, null, 'timed-out reply must stay discarded');
  await flushPromises();
  assert.equal(harness.requests.forwarded.length, 3);
  assert.deepEqual(harness.render().pictures.forwarded.counters, {framesDecoded: 30, bytesReceived: 3000});
  assert.equal(harness.maximumInFlight.forwarded, 1);
});

test('an old success arriving before timeout cannot populate the replacement viewer', async t => {
  const oldRead = deferred(), newRead = deferred();
  const harness = browserHarness((request, call) => request.incarnation > 0 ? newRead.promise :
    call === 4 ? oldRead.promise : report(request.now, call * 1000, call * 10, 'original-receiver'));
  t.after(() => harness.close());
  await harness.start();
  await harness.advanceTo(1750);
  assert.equal(harness.requests.forwarded.length, 4);
  assert.equal(harness.inFlight.forwarded, 1);
  await harness.reconnect();
  assert.equal(harness.requests.forwarded.length, 4);

  // No test time passes: the old result is still within its deadline, but its
  // receiver has been replaced. A timeout check alone cannot reject this reply.
  oldRead.resolve(report(1750, 999999, 9999, 'original-receiver'));
  await flushPromises();
  assert.equal(harness.render().pictures.forwarded.counters, null);
  await flushPromises();
  assert.equal(harness.requests.forwarded.length, 5);
  assert.equal(harness.requests.forwarded.at(-1).incarnation, 1);
  newRead.resolve(report(1750, 70000, 700, 'replacement-receiver'));
  await flushPromises();
  assert.deepEqual(harness.render().pictures.forwarded.counters, {framesDecoded: 700, bytesReceived: 70000});
  assert.equal(harness.maximumInFlight.forwarded, 1);
});
