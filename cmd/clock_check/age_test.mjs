import test from 'node:test';
import assert from 'node:assert/strict';
import {createAge} from './assets/age.mjs';

const session = '0123456789ABCDEF01234567';
const otherSession = 'FEDCBA9876543210FEDCBA98';
const payload = sequence => `FS1:${session}:${sequence}`;
const current = (engine, now, lane = 'local') => engine.inspect(now).lanes[lane].current;
function started(options = {}) {
  const engine = createAge(options);
  engine.start(0, session);
  engine.emit(0, 0);
  engine.emit(100, 1);
  return engine;
}
function reading(engine, sampledAt, options = {}) {
  const {lane = 'local', ...override} = options;
  return engine.read(lane, {sampledAt, observedAt: sampledAt, status: 'decoded', payload: payload(0), ...override});
}

test('marker hold time creates an age interval without adding worker reply time', () => {
  const engine = started();
  const sample = reading(engine, 400, {observedAt: 650}).lanes.local.current;
  assert.deepEqual(sample, {lowerMS: 300, upperMS: 400, midpointMS: 350,
    sequence: 0, sampledAt: 400, observedAt: 650});
  assert.equal('cameraCaptureTime' in sample, false);
  assert.equal('sensorAgeMS' in sample, false);
});

test('an open marker interval has a zero lower bound and a finite upper bound', () => {
  const engine = started();
  const sample = reading(engine, 400, {payload: payload(1)}).lanes.local.current;
  assert.equal(sample.lowerMS, 0);
  assert.equal(sample.upperMS, 300);
  assert.equal(sample.midpointMS, 150);
});

test('a later emission after image sampling cannot create negative age', () => {
  const engine = createAge();
  engine.start(0, session);
  engine.emit(0, 0);
  engine.emit(200, 1);
  const sample = reading(engine, 150, {observedAt: 250}).lanes.local.current;
  assert.equal(sample.lowerMS, 0);
  assert.equal(sample.upperMS, 150);
});

test('a frozen optical marker in new sampled images reports increasing age', () => {
  const engine = started();
  const first = reading(engine, 250).lanes.local.current;
  const second = reading(engine, 1000).lanes.local.current;
  assert.equal(first.midpointMS, 200);
  assert.equal(second.midpointMS, 950);
  assert.equal(first.sequence, second.sequence);
  assert.equal(engine.inspect(1000).lanes.local.summary.valid, 2);
});

test('unreadable, ambiguous and malformed attempts remove current instead of reusing a good reading', () => {
  for (const invalid of [
    {status: 'unreadable'}, {status: 'ambiguous'}, {status: 'worker-failed'},
    {status: undefined}, {payload: 'unrelated QR'}, {payload: null}, {payload: {}},
  ]) {
    const engine = started();
    reading(engine, 200);
    const lane = reading(engine, 300, invalid).lanes.local;
    assert.equal(lane.current, null);
    assert.equal(lane.summary.attempted, 2);
    assert.equal(lane.summary.valid, 1);
    assert.equal(lane.summary.medianMidpointMS, 150, 'prior value survives only in the labelled summary');
  }
});

test('payload parsing is exact, uppercase, bounded and canonical', () => {
  for (const invalid of [
    `FS1:${session.toLowerCase()}:0`, `FS1:${session}:00`, `FS1:${session}:+0`,
    `FS1:${session}:-1`, `FS1:${session}:1.0`, `FS1:${session}:1e0`,
    `FS1:${session}: 0`, ` FS1:${session}:0`, `${payload(0)} `,
    `${payload(0)}\n`, `${payload(0)}\r\n`, `${payload(0)}:extra`,
    `FS2:${session}:0`, `FS1:${session.slice(1)}:0`, `FS1:${session}A:0`,
    `FS1:${session}:9007199254740992`, `FS1:${session}:11111111111111111`,
  ]) {
    const engine = started();
    const lane = reading(engine, 200, {payload: invalid}).lanes.local;
    assert.equal(lane.status, 'invalid-marker', invalid);
    assert.equal(lane.current, null);
    assert.equal(lane.summary.valid, 0);
  }
});

test('wrong-session, unknown and future emissions never become valid measurements', () => {
  for (const [supplied, expected] of [
    [`FS1:${otherSession}:0`, 'wrong-session'], [payload(2), 'unknown-sequence'],
  ]) {
    const engine = started();
    reading(engine, 200);
    const lane = reading(engine, 300, {payload: supplied}).lanes.local;
    assert.equal(lane.status, expected);
    assert.equal(lane.current, null);
    assert.equal(lane.summary.valid, 1);
  }
  const engine = started();
  const lane = reading(engine, 50, {observedAt: 120, payload: payload(1)}).lanes.local;
  assert.equal(lane.status, 'future-marker');
  assert.equal(lane.current, null);
});

test('the result-delay deadline is inclusive at 250 ms and rejects later replies', () => {
  const engine = started();
  assert.ok(reading(engine, 200, {observedAt: 450}).lanes.local.current);
  const lane = reading(engine, 500, {observedAt: 751}).lanes.local;
  assert.equal(lane.status, 'late-result');
  assert.equal(lane.current, null);
  assert.equal(lane.summary.attempted, 2);
  assert.equal(lane.summary.valid, 1);
});

test('older or repeated sampled images cannot replace a newer reading', () => {
  for (const oldSample of [200, 300]) {
    const engine = started();
    reading(engine, 300);
    const lane = reading(engine, oldSample, {observedAt: 350}).lanes.local;
    assert.equal(lane.status, 'out-of-order');
    assert.equal(lane.current, null);
    assert.equal(lane.summary.valid, 1);
  }
});

test('current expires using image sample time, independently of historical summary', () => {
  const engine = started();
  reading(engine, 200, {observedAt: 450});
  assert.ok(current(engine, 1199));
  const lane = engine.inspect(1200).lanes.local;
  assert.equal(lane.status, 'stale');
  assert.equal(lane.current, null);
  assert.equal(lane.summary.medianMidpointMS, 150);
  assert.equal(lane.summary.maxObservedUpperMS, 200);
});

test('a reading in one lane also expires the other lane current value', () => {
  const engine = started();
  reading(engine, 200);
  const state = reading(engine, 1200, {lane: 'forwarded'});
  assert.equal(state.lanes.local.current, null);
  assert.equal(state.lanes.local.status, 'stale');
  assert.equal(state.lanes.local.summary.valid, 1);
  assert.ok(state.lanes.forwarded.current);
});

test('lane invalidation rejects outstanding images but preserves independent observations', () => {
  const engine = started();
  reading(engine, 200);
  reading(engine, 200, {lane: 'forwarded'});
  let state = engine.invalidate('local', 250, 'hidden');
  assert.equal(state.lanes.local.current, null);
  assert.ok(state.lanes.forwarded.current);
  state = reading(engine, 225, {observedAt: 300});
  assert.equal(state.lanes.local.status, 'invalidated-sample');
  assert.equal(state.lanes.local.current, null);
  assert.ok(reading(engine, 350).lanes.local.current);
});

test('stop preserves the final summary, removes current and ignores late worker replies', () => {
  const engine = started();
  reading(engine, 200);
  const done = engine.stop(300);
  assert.equal(done.active, false);
  assert.equal(done.stopReason, 'stopped');
  assert.equal(done.endedAt, 300);
  assert.equal(done.lanes.local.current, null);
  assert.equal(engine.emit(400, 2), null);
  assert.deepEqual(reading(engine, 350, {observedAt: 450}), done);
  assert.deepEqual(engine.inspect(10000), done);
});

test('the session expires at its deadline and cannot accept an expired optical marker', () => {
  const engine = started({durationMS: 1000});
  reading(engine, 500);
  assert.equal(engine.inspect(999).active, true);
  const done = reading(engine, 950, {observedAt: 1000});
  assert.equal(done.active, false);
  assert.equal(done.stopReason, 'time-limit');
  assert.equal(done.endedAt, 1000);
  assert.equal(done.lanes.local.current, null);
  assert.equal(done.lanes.local.summary.attempted, 1, 'work completing after expiry is not part of the finished run');
  assert.equal(engine.emit(1001, 2), null);
});

test('restarting replaces all summaries, emission history and sample epochs', () => {
  const engine = started();
  reading(engine, 200);
  engine.start(500, otherSession);
  engine.emit(500, 0);
  let lane = reading(engine, 550).lanes.local;
  assert.equal(lane.status, 'wrong-session');
  assert.equal(lane.summary.valid, 0);
  lane = reading(engine, 450, {lane: 'forwarded', observedAt: 600, payload: `FS1:${otherSession}:0`}).lanes.forwarded;
  assert.equal(lane.status, 'invalidated-sample');
  assert.equal(lane.current, null);
  const fresh = engine.read('forwarded', {sampledAt: 650, observedAt: 650, status: 'decoded', payload: `FS1:${otherSession}:0`});
  assert.equal(fresh.lanes.forwarded.summary.valid, 1);
  assert.equal(fresh.lanes.forwarded.current.upperMS, 150);
});

test('attempt limits are explicit and summaries never silently discard accepted values', () => {
  const engine = started({maxSamplesPerLane: 3});
  reading(engine, 200);
  reading(engine, 300, {status: 'ambiguous'});
  let lane = reading(engine, 400).lanes.local;
  assert.deepEqual(lane.summary, {attempted: 3, valid: 2, medianMidpointMS: 250,
    maxObservedUpperMS: 400, sampleLimitReached: true});
  lane = reading(engine, 500).lanes.local;
  assert.equal(lane.status, 'sample-limit');
  assert.equal(lane.current, null);
  assert.equal(lane.summary.attempted, 3);
  assert.equal(lane.summary.valid, 2);
  assert.equal(reading(engine, 500, {lane: 'forwarded'}).lanes.forwarded.summary.attempted, 1);
});

test('summary uses valid midpoint readings, with missing attempts counted separately', () => {
  const engine = started();
  reading(engine, 200);
  reading(engine, 300, {status: 'unreadable'});
  reading(engine, 400);
  reading(engine, 1000);
  const summary = engine.inspect(1000).lanes.local.summary;
  assert.equal(summary.attempted, 4);
  assert.equal(summary.valid, 3);
  assert.equal(summary.medianMidpointMS, 350);
  assert.equal(summary.maxObservedUpperMS, 1000);
  assert.equal('p95' in summary, false);
  const empty = engine.inspect(1000).lanes.forwarded.summary;
  assert.equal(empty.medianMidpointMS, null);
  assert.equal(empty.maxObservedUpperMS, null);
});

test('returned objects cannot mutate private current readings or summaries', () => {
  const engine = started();
  const returned = reading(engine, 200);
  returned.lanes.local.current.upperMS = -10;
  returned.lanes.local.summary.valid = 900;
  returned.lanes.local.summary.medianMidpointMS = -10;
  assert.equal(current(engine, 200).upperMS, 200);
  assert.equal(engine.inspect(200).lanes.local.summary.valid, 1);
  assert.equal(engine.inspect(200).lanes.local.summary.medianMidpointMS, 150);
});

test('invalid sampled times remove current without fabricating a negative measurement', () => {
  for (const invalid of [-1, NaN, Infinity, undefined, '300', 301]) {
    const engine = started();
    reading(engine, 200);
    const lane = reading(engine, invalid, {observedAt: 300}).lanes.local;
    assert.equal(lane.status, 'invalid-sample-time');
    assert.equal(lane.current, null);
    assert.equal(lane.summary.valid, 1);
  }
});

test('backwards or invalid observation clock stops the session and clears both current measurements', () => {
  for (const invalid of [199, -1, NaN, Infinity, undefined]) {
    const engine = started();
    reading(engine, 200);
    reading(engine, 200, {lane: 'forwarded'});
    const done = engine.inspect(invalid);
    assert.equal(done.active, false);
    assert.equal(done.stopReason, 'clock-invalid');
    assert.equal(done.endedAt, null);
    assert.equal(done.lanes.local.current, null);
    assert.equal(done.lanes.forwarded.current, null);
    assert.equal(done.lanes.local.summary.valid, 1);
  }
});

test('emissions require increasing unique sequences and rendering times', () => {
  for (const [now, sequence] of [[100, 2], [101, 1], [101, 0], [101, -1], [101, 2.5], [101, Infinity]]) {
    const engine = started();
    assert.equal(engine.emit(now, sequence), null);
    assert.equal(engine.inspect(200).active, false);
    assert.equal(engine.inspect(200).stopReason, 'invalid-emission');
  }
  const engine = started();
  assert.equal(engine.emit(200, Number.MAX_SAFE_INTEGER), payload(Number.MAX_SAFE_INTEGER));
  assert.ok(reading(engine, 300, {payload: payload(Number.MAX_SAFE_INTEGER)}).lanes.local.current);
});

test('marker history has a hard limit instead of evicting old display intervals silently', () => {
  const engine = createAge();
  engine.start(0, session);
  for (let sequence = 0; sequence < 2048; sequence++) assert.equal(engine.emit(sequence, sequence), payload(sequence));
  assert.equal(engine.emit(2048, 2048), null);
  const done = engine.inspect(2048);
  assert.equal(done.active, false);
  assert.equal(done.stopReason, 'marker-limit');
});

test('default sessions and sample counts remain bounded', () => {
  const engine = createAge();
  assert.equal(engine.start(50, session).expiresAt, 120050);
  engine.emit(50, 0);
  for (let i = 1; i <= 512; i++) reading(engine, 50 + i);
  const summary = engine.inspect(562).lanes.local.summary;
  assert.equal(summary.attempted, 512);
  assert.equal(summary.valid, 512);
  assert.equal(summary.sampleLimitReached, true);
});

test('invalid options and reused session identifiers are rejected', () => {
  for (const [key, value] of [
    ['durationMS', 120001], ['durationMS', 0], ['durationMS', Infinity],
    ['maxResultDelayMS', 251], ['maxResultDelayMS', -1], ['currentTTLMS', 1001],
    ['maxSamplesPerLane', 513], ['maxSamplesPerLane', 0], ['maxSamplesPerLane', 1.5],
  ]) assert.throws(() => createAge({[key]: value}), RangeError);
  const engine = createAge();
  for (const [now, token] of [[-1, session], [NaN, session], [0, 'bad'], [0, session.toLowerCase()], [0, session + '\n']])
    assert.throws(() => engine.start(now, token), RangeError);
  engine.start(0, session);
  assert.throws(() => engine.start(100, session), RangeError);
  assert.throws(() => engine.read('unregistered', {}), RangeError);
});
