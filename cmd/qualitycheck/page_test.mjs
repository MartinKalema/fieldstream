import test from 'node:test';
import assert from 'node:assert/strict';
import {mediaChoice, fileSize, sizeChange, similarity, clampTime} from './page.mjs';

test('media paths are a fixed local allowlist, independent of report labels', () => {
  assert.equal(mediaChoice({file:'detail-20.mp4',name:'untrusted label'}).url, '/detail-20.mp4');
  assert.equal(mediaChoice({file:'/small-20.mp4'}).url, '/small-20.mp4');
  for (const file of ['https://example.invalid/video.mp4','//example.invalid/detail-20.mp4','../detail-20.mp4','/private/source.mp4','detail-20.mp4?token=value','original.mp4','playback.mp4']) assert.equal(mediaChoice({file}), null);
  assert.equal(mediaChoice(null), null);
});

test('size change handles increases and unavailable values without fabricating savings', () => {
  assert.equal(sizeChange(1000,650),'35.0% smaller');
  assert.equal(sizeChange(1000,1300),'30.0% larger');
  assert.equal(sizeChange(1000,1000),'About the same');
  for (const size of [undefined,null,NaN,-1]) assert.equal(sizeChange(1000,size),'Unavailable');
  assert.equal(sizeChange(0,10),'Unavailable');
  assert.equal(fileSize(null),'Unavailable');
});

test('similarity remains a numeric index, never a percent-quality claim', () => {
  assert.equal(similarity(0.95),'0.9500');
  assert.equal(similarity(1),'1.0000');
  for (const value of [undefined,null,NaN,1.1,-2]) assert.equal(similarity(value),'Unavailable');
});

test('scrubbing stays within the shared playback duration', () => {
  assert.equal(clampTime(5,3),3);
  assert.equal(clampTime(-1,3),0);
  assert.equal(clampTime(2,3),2);
  assert.equal(clampTime(NaN,3),0);
  assert.equal(clampTime(2,0),0);
});
