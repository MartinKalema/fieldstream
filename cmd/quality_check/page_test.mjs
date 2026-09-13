import test from 'node:test';
import assert from 'node:assert/strict';
import {reportProvenance, mediaChoice, fileSize, sizeChange, similarity, clampTime} from './page.mjs';

test('old reports without scene metadata retain recording provenance', () => {
  assert.deepEqual(reportProvenance({input: {file: 'original.mp4'}}), {kind: 'recording'});
});

test('only known scenes receive generated provenance and their text stays data', () => {
  for (const id of ['fast-motion', 'fine-detail', 'dim-noise']) {
    const title = '<b>Generated title</b>', note = 'Keep this note as text <script>not code</script>.';
    assert.deepEqual(reportProvenance({test_scene: {id, title, note}}), {kind: 'generated', id, title, note});
  }
});

test('unknown or incomplete scene metadata cannot fall back to a camera recording', () => {
  const scene = {id: 'fast-motion', title: 'Generated motion', note: 'A generated scene.'};
  for (const value of [null, undefined, [], 'fast-motion', {}, {...scene, id: 'camera'}, {...scene, id: 'https://example.invalid/fast-motion'}, {...scene, title: ''}, {...scene, title: 'x'.repeat(161)}, {...scene, note: null}, {...scene, note: 'x'.repeat(2001)}]) {
    assert.throws(() => reportProvenance({test_scene: value}), /invalid test scene provenance/);
  }
});

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
