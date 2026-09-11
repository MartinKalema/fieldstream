import {finite, videoSnapshot, publicSnapshot, bufferControl, interval, summary, frameTiming} from './metrics.mjs';

const $ = id => document.getElementById(id);
const sourcePattern = /^[a-z][a-z0-9-]{0,31}$/;
const requestedSource = new URLSearchParams(location.search).get('source');
if (requestedSource && sourcePattern.test(requestedSource)) $('source').value = requestedSource;
if (new URLSearchParams(location.search).get('route') === 'forwarded') $('route').value = 'forwarded';
const beginning = performance.now();
function tickClock() {
  $('clock').textContent = ((performance.now() - beginning) / 1000).toFixed(2).padStart(7, '0');
  requestAnimationFrame(tickClock);
}
tickClock();

let active = null, lastReport = null;
const display = (value, unit = '') => finite(value) ? value.toFixed(2) + unit : 'unavailable';
const mean = values => values.length ? values.reduce((a, b) => a + b, 0) / values.length : null;
const max = values => values.length ? Math.max(...values) : null;
function note(run, message) { if (!run.issues.includes(message)) run.issues.push(message); }

function sampleFrames(run, lane) {
  lane.frameCallbackSupported = typeof lane.video.requestVideoFrameCallback === 'function';
  if (!lane.frameCallbackSupported) return;
  const next = (now, metadata) => {
    if (active !== run || !run.running) return;
    lane.callbackCount++;
    lane.lastCallbackAt = now;
    lane.frameFields.captureTime = finite(metadata.captureTime);
    lane.frameFields.receiveTime = finite(metadata.receiveTime);
    lane.frameFields.processingDuration = finite(metadata.processingDuration);
    if (run.phase === 'measuring') {
      if (lane.frameSamples.length >= 3600) {
        note(run, 'Frame sample limit reached; comparison is inconclusive.');
      } else {
        lane.frameSamples.push({atMS: now - run.measureAt, ...frameTiming(metadata)});
      }
    }
    lane.callbackID = lane.video.requestVideoFrameCallback(next);
  };
  lane.callbackID = lane.video.requestVideoFrameCallback(next);
}

function makeLane(run, position, zero) {
  const video = $(position + '-video');
  video.muted = true;
  const lane = {position, zero, video, reader: null, receivers: new Map(), controls: {},
    stream: new MediaStream(), receiver: null, previous: null, samples: [],
    frameSamples: [], frameFields: {}, callbackID: null, trackCount: 0,
    lastSnapshot: null, liveInterval: null, diagnostics: null, diagnosticSamples: [],
    callbackCount: 0, lastCallbackAt: null,
    state: 'Connecting', errors: 0};
  $(position + '-title').textContent = zero ? 'Zero target request' : 'Browser default';
  video.srcObject = lane.stream;
  sampleFrames(run, lane);
  lane.reader = new MediaMTXWebRTCReader({
    url: run.endpoint,
    onError: () => {
      if (active !== run || !run.running) return;
      lane.errors++;
      lane.state = 'Connection error; any retry invalidates this comparison.';
      note(run, position + ': receiver connection error.');
    },
    onTrack: event => {
      if (active !== run || !run.running) return;
      const kind = event.track.kind;
      const old = lane.receivers.get(kind);
      if (old && old !== event.receiver) note(run, position + ': receiver changed.');
      if (run.phase === 'measuring' && !old) note(run, position + ': a new track arrived during measurement.');
      lane.receivers.set(kind, event.receiver);
      lane.controls[kind] = bufferControl(event.receiver, zero);
      lane.trackCount++;
      if (!lane.stream.getTracks().includes(event.track)) lane.stream.addTrack(event.track);
      if (kind === 'video') {
        lane.receiver = event.receiver;
        lane.previous = null;
        lane.state = 'Receiving video';
      }
      video.play().catch(() => {
        lane.state = 'Playback blocked. Press play, then start a new comparison.';
        note(run, position + ': browser blocked playback.');
      });
    },
  });
  return lane;
}

async function readLane(run, lane) {
  if (!lane.receiver || typeof lane.receiver.getStats !== 'function') return null;
  const receiver = lane.receiver;
  try {
    const stats = await receiver.getStats();
    if (receiver !== lane.receiver) { note(run, lane.position + ': receiver changed during a sample.'); return null; }
    return videoSnapshot(stats);
  } catch {
    if (active === run && run.running) note(run, lane.position + ': receiver statistics failed.');
    return null;
  }
}

function updateDiagnostics(run, lane, snapshot, now) {
  lane.liveInterval = interval(lane.lastSnapshot, snapshot);
  lane.lastSnapshot = snapshot;
  const video = lane.video;
  const track = lane.receiver?.track;
  lane.diagnostics = {
    secondsSinceStart: (now - run.startedAt) / 1000,
    visibilityState: document.visibilityState, documentHasFocus: document.hasFocus(),
    video: {paused: video.paused, ended: video.ended, readyState: video.readyState,
      networkState: video.networkState, currentTime: video.currentTime,
      width: video.videoWidth, height: video.videoHeight, streamActive: lane.stream.active,
      sourceIsExpectedStream: video.srcObject === lane.stream,
      mediaErrorCode: video.error?.code ?? null},
    receiver: track ? {trackReadyState: track.readyState, trackMuted: track.muted,
      trackEnabled: track.enabled, dtlsState: lane.receiver.transport?.state ?? null} : null,
    callbackCountSinceStart: lane.callbackCount,
    millisecondsSinceLastCallback: lane.lastCallbackAt === null ? null : now - lane.lastCallbackAt,
    lastStatsTotals: publicSnapshot(snapshot), lastInterval: lane.liveInterval,
  };
  if (lane.diagnosticSamples.length < 100) lane.diagnosticSamples.push(lane.diagnostics);
  if (lane.receiver && lane.errors === 0) {
    lane.state = lane.liveInterval.valid ? 'Video counters are advancing' :
      'Video is not confirmed moving: ' + lane.liveInterval.reason;
  }
}

function laneFrameSummary(run, lane) {
  const frames = lane.frameSamples;
  const arrivalToDisplay = frames.map(f => f.receiveToExpectedDisplayMS).filter(finite);
  const processing = frames.map(f => f.decodeProcessingMS).filter(finite);
  const gaps = [];
  let previous = 0;
  for (const frame of frames) { gaps.push(frame.atMS - previous); previous = frame.atMS; }
  if (run.measuredMS > 0) gaps.push(Math.max(0, run.measuredMS - previous));
  return {requestVideoFrameCallbackSupported: lane.frameCallbackSupported,
    metadataFieldsExposed: lane.frameFields, callbacksObserved: frames.length,
    receiveToExpectedDisplaySampleCount: arrivalToDisplay.length,
    receiveToExpectedDisplayMeanMS: mean(arrivalToDisplay),
    decodeProcessingMeanMS: mean(processing), maxCallbackGapIncludingEdgesMS: max(gaps),
    note: 'Callback gaps include main-thread scheduling and are not an intact-frame count. Capture time is not used to infer camera latency.'};
}

function renderLane(lane) {
  $(lane.position + '-state').textContent = lane.state;
  const s = summary(lane.samples);
  const control = lane.controls.video;
  $(lane.position + '-stats').textContent = [
    'Video control: ' + (control ? control.status : 'waiting for receiver'),
    'Property: ' + (control?.property ?? 'unavailable'),
    'Readback: ' + (control && 'readback' in control ? String(control.readback) : 'unavailable'),
    'Measured buffer: ' + display(s.jitterBufferDelayMS, ' ms'),
    'Browser target: ' + display(s.jitterBufferTargetDelayMS, ' ms'),
    'Network minimum: ' + display(s.jitterBufferMinimumDelayMS, ' ms'),
    'Mean decode: ' + display(s.decodeMS, ' ms'),
    'Decoded / emitted: ' + (s.decodedFrames ?? '?') + ' / ' + (s.emittedFrames ?? '?'),
    'Dropped frames: ' + (s.framesDropped ?? 'unavailable'),
    'Freezes: ' + (s.freezeCount ?? 'unavailable'),
    'Video frame callback: ' + (lane.frameCallbackSupported ? 'supported in this page' : 'unsupported'),
    'Total received bytes: ' + (lane.lastSnapshot?.bytesReceived ?? 'unavailable'),
    'Total decoded / emitted: ' + (lane.lastSnapshot?.framesDecoded ?? '?') + ' / ' + (lane.lastSnapshot?.jitterBufferEmittedCount ?? '?'),
    'Last video paused / readyState: ' + (lane.diagnostics?.video.paused ?? lane.video.paused) + ' / ' + (lane.diagnostics?.video.readyState ?? lane.video.readyState),
    'Video currentTime: ' + display(lane.diagnostics?.video.currentTime, ' s'),
    'Callbacks since start: ' + lane.callbackCount,
    'Last callback age: ' + display(lane.diagnostics?.millisecondsSinceLastCallback, ' ms'),
  ].join('\n');
  $(lane.position + '-diagnostics').textContent = JSON.stringify(lane.diagnostics, null, 2);
}

async function poll(run) {
  if (active !== run || !run.running) return;
  const snapshots = await Promise.all(run.lanes.map(lane => readLane(run, lane)));
  if (active !== run || !run.running) return;
  const now = performance.now();
  if (document.visibilityState !== 'visible') { finish(run, 'Tab became hidden.'); return; }
  run.lanes.forEach((lane, index) => updateDiagnostics(run, lane, snapshots[index], now));
  const bothMoving = run.lanes.every(lane => lane.liveInterval?.valid && !lane.video.paused && lane.video.readyState >= 2 &&
    (!lane.frameCallbackSupported || (lane.lastCallbackAt !== null && now - lane.lastCallbackAt < 2500)));
  if (run.phase === 'connecting' && bothMoving) {
    run.phase = 'settling'; run.readyAt = now;
  } else if (run.phase === 'settling' && !bothMoving) {
    run.readyAt = now;
  } else if (run.phase === 'settling' && bothMoving && now - run.readyAt >= 10000) {
    run.phase = 'measuring'; run.measureAt = now;
    run.lanes.forEach((lane, index) => { lane.previous = snapshots[index]; });
  } else if (run.phase === 'measuring') {
    run.lanes.forEach((lane, index) => {
      const row = interval(lane.previous, snapshots[index]);
      lane.samples.push(row);
      lane.previous = snapshots[index];
      if (!row.valid) note(run, lane.position + ': ' + row.reason);
      if (lane.video.paused || lane.video.readyState < 2) note(run, lane.position + ': video playback was paused or unavailable.');
      if (lane.frameCallbackSupported && (lane.lastCallbackAt === null || now - lane.lastCallbackAt >= 2500))
        note(run, lane.position + ': displayed-frame callbacks stopped advancing.');
    });
    run.measuredMS = now - run.measureAt;
    if (run.measuredMS >= 30000) { finish(run); return; }
  }
  run.lanes.forEach(renderLane);
  if (run.issues.length) $('result').textContent = 'Comparison already inconclusive: ' + run.issues.join(' ');
  const elapsed = Math.floor((now - run.startedAt) / 1000);
  $('status').textContent = run.phase === 'connecting' ? `Connecting both viewers (${elapsed}s; 90s maximum).` :
    run.phase === 'settling' ? `${bothMoving ? 'Both videos are advancing. Settling' : 'Waiting for continuous playback; settling restarts'}: ${Math.max(0, Math.ceil((10000 - now + run.readyAt) / 1000))}s.` :
    `Measuring both viewers: ${Math.max(0, Math.ceil((30000 - run.measuredMS) / 1000))}s remaining.`;
  run.pollTimer = setTimeout(() => poll(run), 1000);
}

function finish(run, reason = '') {
  if (active !== run || !run.running) return;
  run.running = false;
  clearTimeout(run.pollTimer); clearTimeout(run.deadline);
  if (reason) note(run, reason);
  if (run.phase === 'measuring') run.measuredMS = performance.now() - run.measureAt;
  if (run.measuredMS < 29000 || run.measuredMS > 34000) note(run, 'A full, timely measurement window was not completed.');
  for (const lane of run.lanes) {
    lane.reader.close();
    if (lane.callbackID !== null && typeof lane.video.cancelVideoFrameCallback === 'function') lane.video.cancelVideoFrameCallback(lane.callbackID);
    lane.video.srcObject = null;
    lane.stream.getTracks().forEach(track => track.stop());
    lane.state = 'Connection closed';
    const s = summary(lane.samples);
    if (s.validIntervals < 25 || !(s.emittedFrames > 0)) note(run, lane.position + ': insufficient usable buffer samples.');
    if (lane.zero && (!lane.controls.video || Object.values(lane.controls).some(c => c.status !== 'request-accepted')))
      note(run, lane.position + ': zero request was not accepted on every received audio/video track.');
    renderLane(lane);
  }
  lastReport = {
    schemaVersion: 1, completedAt: new Date().toISOString(), browser: navigator.userAgent,
    route: run.route, zeroTargetPosition: run.zeroLeft ? 'left' : 'right',
    method: 'Two simultaneous receiver.getStats() streams, same selected source. 10s settling, about 30s measurement; weighted deltas, not lifetime averages.',
    measuredSeconds: run.measuredMS / 1000, comparisonUsable: run.issues.length === 0,
    issues: run.issues,
    limits: ['Not camera-to-screen latency or same-source-frame pairing.', 'No original-frame identity or pixel integrity check.',
      'Accepted target readback is not the actual buffer delay.', 'Two viewers add load; repeat with sides swapped.',
      'Missing counters are null, not zero. A short clean run is not a reliability guarantee.',
      'No source URL, credentials, SDP, IP candidates or full raw statistics are exported.'],
    lanes: run.lanes.map(lane => ({position: lane.position, mode: lane.zero ? 'zero-request' : 'browser-default',
      controls: lane.controls, connectionErrors: lane.errors, summary: summary(lane.samples),
      frameCallbacks: laneFrameSummary(run, lane), intervals: lane.samples,
      lastDiagnosticsBeforeClose: lane.diagnostics, diagnosticSamples: lane.diagnosticSamples})),
  };
  $('report').textContent = JSON.stringify(lastReport, null, 2);
  $('save-status').textContent = 'Report is ready to save to the project.';
  $('download').disabled = false;
  if (lastReport.comparisonUsable) {
    const base = lastReport.lanes.find(l => l.mode === 'browser-default').summary.jitterBufferDelayMS;
    const zero = lastReport.lanes.find(l => l.mode === 'zero-request').summary.jitterBufferDelayMS;
    $('result').textContent = `Measured browser buffer: default ${display(base, ' ms')}; zero request ${display(zero, ' ms')}. Check drops and freezes, then repeat with positions swapped. This alone does not establish a safe improvement.`;
  } else $('result').textContent = 'Comparison inconclusive: ' + run.issues.join(' ');
  $('status').textContent = 'Finished. Both diagnostic connections are closed.';
  $('start').disabled = false; $('stop').disabled = true;
  for (const id of ['source', 'route', 'swap']) $(id).disabled = false;
  active = null;
}

$('controls').addEventListener('submit', event => {
  event.preventDefault();
  if (active) return;
  if (!sourcePattern.test($('source').value)) { $('status').textContent = 'Use a valid configured source ID.'; return; }
  if (document.visibilityState !== 'visible' || typeof RTCPeerConnection !== 'function' || typeof MediaMTXWebRTCReader !== 'function') {
    $('status').textContent = 'This page must be visible and its browser must support WebRTC.'; return;
  }
  const route = $('route').value === 'forwarded' ? 'forwarded' : 'local';
  const run = {running: true, startedAt: performance.now(), phase: 'connecting', measuredMS: 0,
    issues: [], lanes: [], route, zeroLeft: $('swap').checked,
    endpoint: new URL(`/whep/${route}/${encodeURIComponent($('source').value)}`, location.origin).href};
  active = run;
  $('start').disabled = true; $('stop').disabled = false; $('download').disabled = true;
  $('report').textContent = 'Measurement in progress.'; $('result').textContent = 'Waiting for a completed comparison.';
  $('save-status').textContent = 'A completed report can be saved to the project.';
  for (const id of ['source', 'route', 'swap']) $(id).disabled = true;
  try {
    run.lanes.push(makeLane(run, 'left', run.zeroLeft));
    run.lanes.push(makeLane(run, 'right', !run.zeroLeft));
    run.deadline = setTimeout(() => finish(run, '90-second run limit reached.'), 90000);
    poll(run);
  } catch {
    finish(run, 'Browser receiver could not be initialized.');
  }
});
$('stop').addEventListener('click', () => { if (active) finish(active, 'Stopped before completion.'); });
document.addEventListener('visibilitychange', () => { if (active && document.visibilityState !== 'visible') finish(active, 'Tab became hidden.'); });
window.addEventListener('pagehide', () => { if (active) finish(active, 'Page closed or navigated away.'); });
$('download').addEventListener('click', async () => {
  if (!lastReport) return;
  const report = lastReport;
  const controller = new AbortController();
  const deadline = setTimeout(() => controller.abort(), 10000);
  $('download').disabled = true;
  $('save-status').textContent = 'Saving the report to the project...';
  try {
    const response = await fetch('/report', {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(report, null, 2), signal: controller.signal});
    if (!response.ok) throw new Error('Save failed (HTTP ' + response.status + '). ' + (await response.text()).slice(0, 300));
    const result = await response.json();
    $('save-status').textContent = 'Saved privately: ' + result.storedPath;
  } catch (error) {
    $('save-status').textContent = error.name === 'AbortError' ? 'Save response timed out; check the project reports directory before retrying.' : error.message;
    if (lastReport === report && !active) $('download').disabled = false;
  } finally { clearTimeout(deadline); }
});
