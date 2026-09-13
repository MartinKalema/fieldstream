import {createAge} from './age.mjs';

// Calibration only: a camera must film this page's direct white clock target.
// One worker, one transferred pixel buffer, no queued frames or remote requests.
const age = createAge();
const routes = ['local', 'forwarded'];
const $ = id => document.getElementById(id);
const lanes = new Map();
const SAMPLE_MS = 500, DEADLINE_MS = 250;
let worker = null, pending = null, jobID = 0, epoch = 0;
let sequence = 0, lastMarker = -Infinity, lastObservation = null;
let timer, animation, closed = false, preferred = 'local';
let session = null, notice = 'Point the camera only at the white clock and pattern. Then start the test.';
let timeoutStreak = 0;
const target = $('delay-marker');
const targetContext = target.getContext('2d', {alpha: false});

function put(id, value) { if ($(id).textContent !== value) $(id).textContent = value; }
function usable(lane) {
  return document.visibilityState === 'visible' && !lane.video.paused && !lane.video.ended &&
    !lane.video.error && lane.video.readyState >= 2 &&
    $(lane.route + '-panel').dataset.state === 'advancing';
}
function videoTrack(video) {
  const tracks = video.srcObject?.getVideoTracks?.();
  return tracks?.length === 1 ? tracks[0] : null;
}
function sameSource(lane) {
  return lane.stream === lane.video.srcObject && lane.track !== null && lane.track === videoTrack(lane.video);
}
function sourceChanged() {
  finish('A viewer reconnected. Start a new test to keep results separate.');
}
function terminateWorker() {
  worker?.terminate(); worker = null;
  if (pending) clearTimeout(pending.timer);
  pending = null;
}
function clearPattern() {
  if (targetContext) {
    targetContext.fillStyle = '#fff'; targetContext.fillRect(0, 0, target.width, target.height);
  }
  target.hidden = true;
}
function finish(message) {
  lastObservation = null;
  if (!age.inspect(performance.now()).active) {
    terminateWorker(); clearPattern();
    if (message) notice = message;
    return;
  }
  // Stop invalidates current readings, retaining only this completed run's summary.
  age.stop(performance.now()); epoch++;
  terminateWorker(); clearPattern();
  notice = message || 'Test finished. The summary below contains successful samples only.';
}
// Every asynchronous observer checks the same scheduling boundary. The first
// callback after a suspension must stop the run before it can append a sample;
// that callback need not be requestAnimationFrame.
function observe(now) {
  if (!age.inspect(now).active) return false;
  if (lastObservation !== null && now - lastObservation > 1000) {
    finish('Browser observation was interrupted. Start a new test.');
    return false;
  }
  if ([...lanes.values()].some(lane => !sameSource(lane))) {
    sourceChanged();
    return false;
  }
  lastObservation = now;
  return true;
}
function reply(result) {
  const job = pending;
  if (!job || result?.id !== job.id) return;
  clearTimeout(job.timer); pending = null;
  const now = performance.now();
  if (job.epoch !== epoch || !observe(now)) return;
  const lane = lanes.get(job.route);
  if (!sameSource(lane) || job.track !== videoTrack(lane.video)) { sourceChanged(); return; }
  if (now - job.sampledAt > DEADLINE_MS || !usable(lane)) {
    age.read(job.route, {sampledAt: job.sampledAt, observedAt: now, status: 'unreadable'});
    lane.failures++; return;
  }
  timeoutStreak = 0;
  lane.lastDecodeMS = now - job.sampledAt;
  age.read(job.route, {sampledAt: job.sampledAt, observedAt: now,
    status: result.status === 'read' ? 'decoded' : result.status === 'ambiguous' ? 'ambiguous' : 'unreadable',
    payload: result.payload});
  if (result.status !== 'read') lane.failures++;
}
function ensureWorker() {
  if (worker) return true;
  try {
    const instance = new Worker('/qr-worker.js'), workerEpoch = epoch;
    worker = instance;
    instance.onmessage = event => {
      if (worker === instance && epoch === workerEpoch && !closed) reply(event.data);
    };
    instance.onerror = () => {
      if (worker === instance && epoch === workerEpoch && !closed && observe(performance.now()))
        finish('Pattern reader failed. Start a new test to try again.');
    };
    return true;
  } catch {
    finish('This browser cannot start the background pattern reader. Manual clock comparison is still available.');
    return false;
  }
}
function drawMarker(now) {
  const payload = `FS1:${session}:${sequence}`;
  const qr = globalThis.QRCode.create(payload, {errorCorrectionLevel: 'M'});
  const size = qr.modules.size, scale = Math.floor(target.width / (size + 8));
  const offset = Math.floor((target.width - size * scale) / 2);
  if (scale < 1) throw new Error('Marker too large');
  targetContext.fillStyle = '#fff'; targetContext.fillRect(0, 0, target.width, target.height);
  targetContext.fillStyle = '#000';
  for (let row = 0; row < size; row++) for (let col = 0; col < size; col++) {
    if (qr.modules.get(row, col)) targetContext.fillRect(offset + col * scale, offset + row * scale, scale, scale);
  }
  target.hidden = false;
  // Record real draw time, not a presumed 100 ms schedule. Physical screen
  // illumination/exposure can differ, so reported intervals are approximate.
  const drawnAt = performance.now();
  if (age.emit(drawnAt, sequence) !== payload) throw new Error('Marker session ended');
  sequence++; lastMarker = drawnAt;
}
function animate() {
  if (closed) return;
  const now = performance.now();
  if (observe(now)) {
    if (now - lastMarker >= 100) {
      try { drawMarker(now); } catch { finish('Could not draw the test pattern. Start a new test.'); }
    }
  }
  animation = requestAnimationFrame(animate);
}
function attempt(lane, metadata) {
  const now = performance.now();
  lane.lastFrameAt = now;
  if (!observe(now)) return;
  if (!sameSource(lane)) { sourceChanged(); return; }
  if (!usable(lane) || now < lane.nextDue) return;
  // A frame callback's media clock is not original camera capture time. Use
  // our monotonic clock immediately before copying its pixels. Reject severely
  // late callbacks rather than interpreting browser scheduling as precise age.
  if (!Number.isFinite(metadata.expectedDisplayTime) || Math.abs(now - metadata.expectedDisplayTime) > 100) {
    age.invalidate(lane.route, now, 'Presentation timing unavailable'); lane.timingFailures++; lane.nextDue = now + SAMPLE_MS; return;
  }
  if (pending) {
    if (now - lane.lastBusyAt >= SAMPLE_MS) { lane.busyChecks++; lane.lastBusyAt = now; }
    return;
  }
  const other = lanes.get(lane.route === 'local' ? 'forwarded' : 'local');
  if (preferred !== lane.route && usable(other) && now >= other.nextDue && now - other.lastFrameAt < 250) return;
  lane.nextDue = now + SAMPLE_MS;
  preferred = other.route;
  if (!ensureWorker()) return;
  const ratio = Math.min(1, 960 / lane.video.videoWidth, 540 / lane.video.videoHeight);
  const width = Math.floor(lane.video.videoWidth * ratio), height = Math.floor(lane.video.videoHeight * ratio);
  if (width < 1 || height < 1) { age.invalidate(lane.route, now, 'No video pixels'); return; }
  if (lane.canvas.width !== width || lane.canvas.height !== height) { lane.canvas.width = width; lane.canvas.height = height; }
  const sampledAt = performance.now();
  let pixels;
  try {
    lane.context.drawImage(lane.video, 0, 0, width, height);
    pixels = lane.context.getImageData(0, 0, width, height).data;
  } catch { finish('This browser cannot read the video pixels. Manual clock comparison is still available.'); return; }
  const id = ++jobID;
  const job = {id, route: lane.route, sampledAt, stream: lane.stream, track: lane.track, epoch};
  job.timer = setTimeout(() => {
    if (pending !== job) return;
    const observedAt = performance.now();
    if (!observe(observedAt)) return;
    if (!sameSource(lane)) { sourceChanged(); return; }
    age.read(lane.route, {sampledAt, observedAt, status: 'unreadable'});
    lane.timeouts++; timeoutStreak++;
    terminateWorker();
    if (timeoutStreak >= 5) finish('Pattern reading is too slow on this browser. The test stopped; try framing only the white target.');
  }, Math.max(0, DEADLINE_MS - (performance.now() - sampledAt)));
  pending = job;
  // Count work that starts even if posting fails or stopping cancels its reply.
  // Completed engine attempts alone would hide those missing measurements.
  lane.readsStarted++;
  try { worker.postMessage({id, width, height, buffer: pixels.buffer}, [pixels.buffer]); }
  catch { finish('The background pattern reader could not accept a frame. Start a new test.'); }
}
function watchVideo(lane) {
  const next = (_now, metadata) => {
    if (closed) return;
    attempt(lane, metadata);
    lane.callbackID = lane.video.requestVideoFrameCallback(next);
  };
  if (typeof lane.video.requestVideoFrameCallback === 'function') lane.callbackID = lane.video.requestVideoFrameCallback(next);
}
function startRun() {
  const now = performance.now();
  if (!targetContext || !globalThis.QRCode?.create || !globalThis.crypto?.getRandomValues ||
      routes.some(route => !lanes.get(route).context || typeof lanes.get(route).video.requestVideoFrameCallback !== 'function')) {
    notice = 'Automatic measurement is unavailable in this browser. Use the manual clock.'; return;
  }
  if (routes.some(route => !usable(lanes.get(route)) || !videoTrack(lanes.get(route).video))) {
    notice = 'Wait until both pictures are advancing, then start the test.'; return;
  }
  terminateWorker(); epoch++; sequence = 0; timeoutStreak = 0; lastMarker = -Infinity; lastObservation = now; preferred = 'local';
  const bytes = crypto.getRandomValues(new Uint8Array(12));
  session = [...bytes].map(value => value.toString(16).padStart(2, '0')).join('').toUpperCase();
  age.start(now, session);
  for (const lane of lanes.values()) {
    Object.assign(lane, {stream: lane.video.srcObject, track: videoTrack(lane.video), nextDue: now + 500, lastBusyAt: -Infinity,
      readsStarted: 0, busyChecks: 0, unavailableChecks: 0, timingFailures: 0, failures: 0, timeouts: 0, lastDecodeMS: null});
  }
  notice = 'Test running. Film only the white clock and pattern; keep other copies of the pattern outside the picture.';
  if (ensureWorker()) { try { drawMarker(now); } catch { finish('Could not draw the test pattern.'); } }
}
function formatMS(value) { return value === null ? '—' : (value / 1000).toFixed(2) + ' s'; }
const statusText = {
  waiting: 'Start the automatic test, then keep the pattern readable.',
  finished: 'Test finished. No current measurement is kept.',
  invalidated: 'Playback observation is unavailable.',
  stale: 'No recent successful pattern reading.',
  unreadable: 'Pattern not readable. Film only the white target and hold the camera steady.',
  ambiguous: 'More than one pattern was found. Keep other copies outside the camera picture.',
  'sample-limit': 'The sample limit has been reached.',
  'invalid-sample-time': 'The sample time could not be verified.',
  'invalidated-sample': 'This sample belongs to an interrupted observation.',
  'out-of-order': 'An older reader response was discarded.',
  'late-result': 'Reading the pattern took too long.',
  'invalid-marker': 'The pattern does not have the expected test format.',
  'wrong-session': 'This pattern belongs to another test.',
  'unknown-sequence': 'The pattern could not be matched to this test.',
  'future-marker': 'The pattern timing could not be matched to this sample.',
};
function render() {
  if (closed) return;
  const now = performance.now();
  observe(now);
  let snapshot = age.inspect(now);
  if (!snapshot.active && !target.hidden) finish('Test finished. The summary below contains successful samples only.');
  if (snapshot.active) {
    for (const lane of lanes.values()) {
      if (!sameSource(lane)) { sourceChanged(); break; }
      if (!usable(lane)) {
        age.invalidate(lane.route, now, 'Playback observation unavailable'); lane.unavailableChecks++;
      }
    }
    snapshot = age.inspect(now);
  }
  put('age-notice', notice);
  put('age-remaining', snapshot.active ? Math.max(0, Math.ceil((snapshot.expiresAt - now) / 1000)) + ' seconds left' : '');
  $('age-start').disabled = snapshot.active;
  $('age-stop').disabled = !snapshot.active;
  const details = {method: 'Optical calibration; approximate marker interval, not verified sensor capture time', active: snapshot.active,
    maximumRunSeconds: 120, maximumSamplesPerSecondPerPicture: 2, workerDeadlineMS: DEADLINE_MS, pictures: {}};
  for (const route of routes) {
    const state = snapshot.lanes[route], lane = lanes.get(route), current = state.current;
    const currentText = current ? formatMS(current.lowerMS) + ' – ' + formatMS(current.upperMS) : 'Measurement unavailable';
    put(route + '-age-current', currentText);
    put(route + '-age-reason', current ? 'Latest sampled frame · read ' + ((now - current.sampledAt) / 1000).toFixed(1) + ' s ago' :
      (statusText[state.status] || 'Measurement unavailable. Start a new test.'));
    put(route + '-age-summary', 'Successful reads: ' + state.summary.valid + '/' + (lane.readsStarted || 0) +
      ' · Typical midpoint: ' + formatMS(state.summary.medianMidpointMS) +
      ' · Largest sampled upper value: ' + formatMS(state.summary.maxObservedUpperMS));
    details.pictures[route] = {...state, startedReads: lane.readsStarted || 0,
      completedAttempts: state.summary.attempted, inFlight: pending?.route === route,
      busyChecksSkipped: lane.busyChecks || 0,
      unavailablePlaybackChecks: lane.unavailableChecks || 0, latePresentationChecks: lane.timingFailures || 0,
      workerTimeouts: lane.timeouts || 0, latestReadWorkMS: lane.lastDecodeMS ?? null};
  }
  put('age-diagnostics', JSON.stringify(details, null, 2));
  timer = setTimeout(render, 250);
}
for (const route of routes) {
  const canvas = document.createElement('canvas');
  const lane = {route, video: $(route + '-video'), canvas, context: canvas.getContext('2d', {willReadFrequently: true}),
    stream: null, track: null, nextDue: 0, callbackID: null, lastFrameAt: -Infinity};
  lanes.set(route, lane);
}
for (const lane of lanes.values()) watchVideo(lane);
$('age-start').addEventListener('click', startRun);
$('age-stop').addEventListener('click', () => finish('Stopped. Summary kept; start another test to clear it.'));
for (const route of routes) $(route + '-reconnect').addEventListener('click', () => {
  if (age.inspect(performance.now()).active) finish('A viewer reconnected. Start a new test to keep results separate.');
});
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState !== 'visible' && age.inspect(performance.now()).active) finish('The page was hidden. Start a new test when it is visible.');
});
window.addEventListener('pagehide', () => {
  finish('The page was left. Start a new test.'); closed = true;
  clearTimeout(timer); cancelAnimationFrame(animation);
  for (const lane of lanes.values()) if (lane.callbackID !== null) lane.video.cancelVideoFrameCallback?.(lane.callbackID);
});
window.addEventListener('pageshow', event => {
  if (event.persisted && closed) {
    closed = false; lastObservation = null;
    for (const lane of lanes.values()) watchVideo(lane);
    animate(); render();
  }
});
animate(); render();
