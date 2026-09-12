import test from 'node:test';
import assert from 'node:assert/strict';
import {createWatch} from './assets/watch.mjs';

const active = {connected: true};
const snapshot = (timestamp, bytesReceived = timestamp * 100, framesDecoded = timestamp / 10, id = 'receiver') =>
  ({id, timestamp, bytesReceived, framesDecoded});

function ready() {
  const watch = createWatch();
  watch.reset(0);
  watch.inspect(0, active);
  for (let now = 0; now <= 1200; now += 100) watch.frame(now, now / 1000, now / 100 + 1);
  assert.equal(watch.inspect(1200, active).state, 'advancing');
  return watch;
}

function resume(watch, start = 1500) {
  watch.frame(start, 100, 100);
  watch.frame(start + 100, 100.1, 101);
  assert.equal(watch.inspect(start + 100, active).state, 'recovering');
  for (let elapsed = 200; elapsed <= 1100; elapsed += 100) watch.frame(start + elapsed, 100 + elapsed / 1000, 100 + elapsed / 100);
  assert.equal(watch.inspect(start + 1100, active).state, 'advancing');
}

test('connected without a first picture warns at the threshold, with no invented picture age', () => {
  const watch = createWatch();
  watch.reset(0);
  assert.equal(watch.inspect(0, active).state, 'starting');
  assert.equal(watch.inspect(1499, active).state, 'starting');
  const result = watch.inspect(1500, active);
  assert.equal(result.state, 'stalled');
  assert.equal(result.frameAgeMS, null);
  assert.equal(result.evidence, 'unavailable');
});

test('initial observations need advancing timestamps and a sustained run', () => {
  const watch = createWatch();
  watch.reset(0);
  watch.frame(0, 5, 1);
  assert.equal(watch.inspect(0, active).state, 'starting');
  watch.frame(100, 5.1, 2);
  assert.equal(watch.inspect(100, active).state, 'recovering');
  // Waiting alone must not turn a single advancing callback into recovery.
  assert.equal(watch.inspect(1099, active).state, 'recovering');
  watch.frame(1099, 6.1, 3);
  assert.equal(watch.inspect(1099, active).state, 'recovering');
  watch.frame(1100, 6.2, 4);
  assert.equal(watch.inspect(1100, active).state, 'advancing');
});

test('repeated same-time callbacks do not hide a stall even if their counters increase', () => {
  const watch = ready();
  for (let now = 1300; now <= 2700; now += 100) watch.frame(now, 1.2, now / 100 + 1);
  const result = watch.inspect(2700, active);
  assert.equal(result.state, 'stalled');
  assert.equal(result.frameAgeMS, 1500);
});

test('advancing media time with a repeated presentation counter does not count', () => {
  const watch = ready();
  for (let now = 1300; now <= 2700; now += 100) watch.frame(now, now / 1000, 13);
  assert.equal(watch.inspect(2700, active).state, 'stalled');
});

test('invalid or missing callback metadata cannot keep a picture healthy', () => {
  for (const [media, frames] of [[NaN, 14], [Infinity, 14], [-1, 14], [1.3, undefined],
    [1.3, 0], [1.3, -1], [1.3, 1.5], [1.3, Number.MAX_SAFE_INTEGER + 1], ['1.3', 14]]) {
    const watch = ready();
    watch.frame(2000, media, frames);
    assert.equal(watch.inspect(2700, active).state, 'stalled');
  }
});

test('static scene content is irrelevant when browser timestamps advance', () => {
  const watch = ready();
  assert.equal(watch.inspect(1200, active).state, 'advancing');
  assert.match(watch.inspect(1200, active).detail, /age is not verified/);
  // The watcher has no image/content comparison and cannot infer capture age.
  const oldRecording = createWatch();
  oldRecording.reset(0);
  for (let now = 0; now <= 1200; now += 100) oldRecording.frame(now, 8000 + now / 1000, now / 100 + 1);
  const result = oldRecording.inspect(1200, active);
  assert.equal(result.state, 'advancing');
  assert.equal(result.frameAgeMS, 0);
  assert.equal('captureAgeMS' in result, false);
  assert.equal('cameraLatency' in result, false);
});

test('a warning requires sustained recovery, not a single returning frame', () => {
  const watch = ready();
  assert.equal(watch.inspect(2700, active).state, 'stalled');
  watch.frame(2800, 2.8, 28);
  assert.equal(watch.inspect(2800, active).state, 'recovering');
  assert.equal(watch.inspect(3800, active).state, 'recovering');
  watch.frame(3800, 3.8, 38);
  assert.equal(watch.inspect(3800, active).state, 'advancing');
});

test('a stall during recovery restarts the recovery interval including at the boundary', () => {
  const watch = ready();
  watch.inspect(2700, active);
  watch.frame(2800, 2.8, 28);
  watch.frame(4300, 4.3, 43);
  assert.equal(watch.inspect(4300, active).state, 'recovering');
  watch.frame(5299, 5.299, 53);
  assert.equal(watch.inspect(5299, active).state, 'recovering');
  watch.frame(5300, 5.3, 54);
  assert.equal(watch.inspect(5300, active).state, 'advancing');
});

test('a returning frame detects an unpolled stall without incorrectly retaining a prior healthy run', () => {
  const watch = ready();
  watch.frame(2700, 2.7, 27);
  assert.equal(watch.inspect(2700, active).state, 'recovering');
});

test('bytes or decoded frames can explain a stall but cannot clear it', () => {
  for (const [bytes, decoded, evidence] of [
    [2000, 10, 'network-bytes-advancing'], [2000, 20, 'decoded-frames-advancing'],
    [1000, 10, 'no-counter-progress'],
  ]) {
    const watch = ready();
    watch.stats(1700, snapshot(100, 1000, 10));
    watch.stats(2200, snapshot(200, bytes, decoded));
    const result = watch.inspect(2700, active);
    assert.equal(result.state, 'stalled');
    assert.equal(result.evidence, evidence);
    assert.equal(result.frameAgeMS, 1500);
  }
});

test('a no-picture connection still warns while every network counter advances', () => {
  const watch = createWatch();
  watch.reset(0);
  for (let now = 0; now <= 1500; now += 500) watch.stats(now, snapshot(now));
  const result = watch.inspect(1500, active);
  assert.equal(result.state, 'stalled');
  assert.equal(result.evidence, 'decoded-frames-advancing');
  assert.equal(result.frameAgeMS, null);
});

test('missing, thrown, malformed or stale statistics are unavailable rather than zero', () => {
  const watch = ready();
  watch.stats(1200, snapshot(100));
  watch.stats(1300, snapshot(200));
  assert.equal(watch.inspect(1300, active).evidence, 'decoded-frames-advancing');
  // The caller converts a rejected getStats promise or timeout to null.
  for (const missing of [null, undefined, {}, {id: 'x'}, snapshot(NaN), snapshot(400, -1), snapshot(400, 500, 1.5)]) {
    watch.stats(1400, missing);
    const result = watch.inspect(1400, active);
    assert.equal(result.evidence, 'unavailable');
    assert.equal(result.state, 'advancing');
  }
  watch.stats(1500, snapshot(300));
  assert.equal(watch.inspect(1500, active).evidence, 'unavailable', 'first sample after missing statistics is only a baseline');
  watch.stats(1600, snapshot(400));
  assert.equal(watch.inspect(1600, active).evidence, 'decoded-frames-advancing');
  watch.inspect(2600, active);
  assert.equal(watch.inspect(3601, active).evidence, 'unavailable');
});

test('duplicate statistics timestamps provide no evidence of counter progress', () => {
  const watch = ready();
  watch.stats(1300, snapshot(100, 1000, 10));
  watch.stats(1400, snapshot(100, 2000, 20));
  assert.equal(watch.inspect(1400, active).evidence, 'unavailable');
});

test('statistics identity, timestamp and counter resets invalidate picture continuity', () => {
  for (const changed of [snapshot(200, 2000, 20, 'new-receiver'), snapshot(99, 2000, 20),
    snapshot(200, 999, 20), snapshot(200, 2000, 9)]) {
    const watch = ready();
    watch.stats(1300, snapshot(100, 1000, 10));
    watch.stats(1400, changed);
    const result = watch.inspect(1400, active);
    assert.equal(result.state, 'recovering');
    assert.equal(result.frameAgeMS, null);
    assert.equal(result.evidence, 'unavailable');
    resume(watch);
  }
});

test('statistics identity remains guarded across a missing report', () => {
  const watch = ready();
  watch.stats(1200, snapshot(100));
  watch.stats(1250, null);
  watch.stats(1300, snapshot(200, 20000, 20, 'replacement'));
  assert.equal(watch.inspect(1300, active).state, 'recovering');
});

test('caller mutation of a stats object cannot alter the private baseline or leak fields', () => {
  const watch = ready();
  const input = {...snapshot(100), secret: 'never serialize this'};
  watch.stats(1300, input);
  input.id = 'changed';
  input.framesDecoded = 100000;
  watch.stats(1400, snapshot(200));
  const result = watch.inspect(1400, active);
  assert.equal(result.state, 'advancing');
  assert.equal(result.evidence, 'decoded-frames-advancing');
  assert.equal(JSON.stringify(result).includes('secret'), false);
  assert.equal(JSON.stringify(result).includes('receiver'), false);
});

test('media timestamp and presentation counter resets require new progress', () => {
  for (const [media, frames] of [[0, 14], [1.3, 1]]) {
    const watch = ready();
    watch.frame(1300, media, frames);
    assert.equal(watch.inspect(1300, active).state, 'recovering');
    resume(watch);
  }
});

test('every observation interruption ignores callbacks until active observation resumes', () => {
  for (const [options, expected] of [
    [{visible: false}, 'hidden'], [{supported: false}, 'unavailable'],
    [{observationAvailable: false}, 'unavailable'], [{paused: true}, 'paused'],
    [{ended: true}, 'ended'], [{connected: false}, 'unavailable'],
  ]) {
    const watch = ready();
    let result = watch.inspect(1300, {...active, ...options});
    assert.equal(result.state, expected);
    assert.equal(result.frameAgeMS, null);
    watch.frame(1400, 1.4, 14);
    watch.stats(1400, snapshot(100));
    assert.equal(watch.inspect(1400, {...active, ...options}).state, expected);
    result = watch.inspect(1500, active);
    assert.equal(result.state, 'recovering');
    assert.equal(result.frameAgeMS, null);
    assert.equal(result.evidence, 'unavailable');
    resume(watch, 1600);
  }
});

test('connected is required explicitly and never stands in for picture progress', () => {
  const watch = ready();
  assert.equal(watch.inspect(1200).state, 'unavailable');
});

test('long observation gaps invalidate old continuity and need new callbacks', () => {
  const watch = ready();
  const result = watch.inspect(3201, active);
  assert.equal(result.state, 'recovering');
  assert.equal(result.frameAgeMS, null);
  assert.equal(result.evidence, 'unavailable');
  resume(watch, 3300);
});

test('gap boundary is inclusive; a permitted gap can still reveal a stall', () => {
  const watch = ready();
  assert.equal(watch.inspect(3200, active).state, 'stalled');
  assert.equal(watch.inspect(3200, active).frameAgeMS, 2000);
});

test('backwards or invalid observation time never produces negative age or preserved health', () => {
  for (const badNow of [1199, -1, NaN, Infinity, undefined, '1300']) {
    const watch = ready();
    const result = watch.inspect(badNow, active);
    assert.equal(result.state, 'unavailable');
    assert.equal(result.frameAgeMS, null);
    assert.equal(result.evidence, 'unavailable');
    assert.equal(watch.inspect(1400, active).state, 'recovering');
    resume(watch);
  }
});

test('reset removes old frame and counter evidence', () => {
  const watch = ready();
  watch.stats(1300, snapshot(100));
  watch.stats(1400, snapshot(200));
  watch.reset(1500);
  const result = watch.inspect(1500, active);
  assert.equal(result.state, 'starting');
  assert.equal(result.frameAgeMS, null);
  assert.equal(result.evidence, 'unavailable');
  assert.equal(watch.inspect(3000, active).state, 'stalled');
});

test('uninitialized and invalidly initialized watches require bounded fresh observation', () => {
  const watch = createWatch();
  assert.equal(watch.inspect(500, active).state, 'starting');
  assert.equal(watch.inspect(2000, active).state, 'stalled');
  watch.reset(NaN);
  assert.equal(watch.inspect(3000, active).state, 'recovering');
});

test('custom interval limits take effect at exact thresholds', () => {
  const watch = createWatch({stallMS: 100, recoverMS: 50, observationGapMS: 200});
  watch.reset(0);
  watch.frame(0, 0, 1);
  watch.frame(10, 0.01, 2);
  watch.frame(59, 0.059, 3);
  assert.equal(watch.inspect(59, active).state, 'recovering');
  watch.frame(60, 0.06, 4);
  assert.equal(watch.inspect(60, active).state, 'advancing');
  assert.equal(watch.inspect(159, active).state, 'advancing');
  assert.equal(watch.inspect(160, active).state, 'stalled');
});

test('invalid configured intervals are rejected', () => {
  for (const key of ['stallMS', 'recoverMS', 'observationGapMS']) {
    for (const value of [0, -1, NaN, Infinity, '1000']) assert.throws(() => createWatch({[key]: value}), RangeError);
  }
});

test('result states, evidence and copy stay fixed and contain only bounded public data', () => {
  const watch = ready();
  const allowed = new Set(['starting', 'advancing', 'stalled', 'paused', 'unavailable', 'recovering', 'hidden', 'ended']);
  const evidence = new Set(['decoded-frames-advancing', 'network-bytes-advancing', 'no-counter-progress', 'unavailable']);
  for (const options of [active, {...active, visible: false}, {...active, paused: true}, {...active, ended: true}, {connected: false}]) {
    const result = watch.inspect(1300, options);
    assert.deepEqual(Object.keys(result).sort(), ['detail', 'evidence', 'frameAgeMS', 'state', 'title']);
    assert.ok(allowed.has(result.state));
    assert.ok(evidence.has(result.evidence));
    assert.equal(typeof result.title, 'string');
    assert.equal(typeof result.detail, 'string');
    assert.ok(result.detail.length < 250);
    assert.doesNotMatch(result.title, /\bLIVE\b|fresh|recent/i);
  }
});
