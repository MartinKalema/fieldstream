import test from 'node:test';
import assert from 'node:assert/strict';
import {videoSnapshot, publicSnapshot, bufferControl, interval, summary, frameTiming} from './assets/metrics.mjs';

test('unsupported receivers are never given fake expando controls', () => {
  const receiver = {getStats() {}};
  assert.equal(bufferControl(receiver, true).status, 'unsupported');
  assert.equal('jitterBufferTarget' in receiver, false);
  assert.equal('playoutDelayHint' in receiver, false);
});
test('default leaves setters untouched; zero records acceptance or failure', () => {
  let value = null, writes = 0;
  const receiver = {get jitterBufferTarget() { return value; }, set jitterBufferTarget(v) { value = v; writes++; }};
  assert.equal(bufferControl(receiver, false).status, 'browser-default');
  assert.equal(writes, 0);
  assert.equal(bufferControl(receiver, true).status, 'request-accepted');
  assert.equal(writes, 1);
  const broken = {get jitterBufferTarget() { return null; }, set jitterBufferTarget(_) { throw new TypeError('private details'); }};
  assert.deepEqual(bufferControl(broken, true).errorName, 'TypeError');
  assert.equal(JSON.stringify(bufferControl(broken, true)).includes('private details'), false);
  assert.equal(bufferControl({playoutDelayHint: null}, true).unit, 'seconds');
});
test('buffer mean is weighted by emitted frames and absent stats stay null', () => {
  const a = {id:'a',timestamp:0,jitterBufferEmittedCount:100,framesDecoded:100,jitterBufferDelay:3};
  const b = {...a,timestamp:1000,jitterBufferEmittedCount:110,framesDecoded:110,jitterBufferDelay:3.2};
  const c = {...a,timestamp:2000,jitterBufferEmittedCount:140,framesDecoded:140,jitterBufferDelay:6.2};
  const s = summary([interval(a,b),interval(b,c)]);
  assert.ok(Math.abs(s.jitterBufferDelayMS - 80) < 0.00001);
  assert.equal(s.framesDropped, null);
  assert.equal(s.jitterBufferTargetDelayMS, null);
  assert.equal(s.decodedFrames, 40);
});
test('reset, missing video and paused sampling cannot produce a valid comparison', () => {
  const a = {id:'a',timestamp:1,jitterBufferEmittedCount:100,framesDecoded:100,jitterBufferDelay:3};
  assert.equal(interval(a,{...a,id:'b',timestamp:1001}).valid,false);
  assert.equal(interval(a,{...a,timestamp:4000}).valid,false);
  assert.equal(interval(a,{...a,timestamp:1001,framesDecoded:1}).valid,false);
  assert.equal(interval(a,null).valid,false);
  const s = videoSnapshot(new Map([['video',{...a,type:'inbound-rtp',kind:'video',secret:'private',ssrc:123}]]));
  assert.equal(s.secret,undefined); assert.equal(s.ssrc,undefined);
  assert.equal(publicSnapshot(s).id, undefined);
});
test('advancing stats timestamps with frozen video do not count as valid intervals', () => {
  const a = {id:'a',timestamp:1,jitterBufferEmittedCount:100,framesDecoded:100,jitterBufferDelay:3,bytesReceived:1000};
  const b = {...a,timestamp:1001,bytesReceived:2000};
  const row = interval(a,b);
  assert.equal(row.valid,false);
  assert.equal(row.decoded,0);
  assert.equal(row.emitted,0);
  assert.equal(row.bytesReceived,1000);
  assert.match(row.reason,/did not both advance/);
  assert.equal(summary([row]).validIntervals,0);
});
test('optional frame clocks never claim absolute camera latency', () => {
  assert.equal(frameTiming({captureTime:1,expectedDisplayTime:100}).receiveToExpectedDisplayMS,null);
  assert.equal(frameTiming({receiveTime:120,expectedDisplayTime:100}).receiveToExpectedDisplayMS,null);
  assert.equal(frameTiming({receiveTime:80,expectedDisplayTime:100}).receiveToExpectedDisplayMS,20);
  assert.equal('cameraLatency' in frameTiming({captureTime:1,expectedDisplayTime:100}),false);
});
