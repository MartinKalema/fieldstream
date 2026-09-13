import {createWatch} from './watch.mjs';

const $ = id => document.getElementById(id);
const finite = value => typeof value === 'number' && Number.isFinite(value);
const beginning = performance.now();
const lanes = [];
const events = [];
let closed = false;
let pollTimer;
let clockRequest;
const labels = {local: 'Local picture', forwarded: 'Forwarded picture'};

function text(id, value) {
  if ($(id).textContent !== value) $(id).textContent = value;
}
function clockTick() {
  if (closed) return;
  text('clock', ((performance.now() - beginning) / 1000).toFixed(2).padStart(7, '0'));
  clockRequest = requestAnimationFrame(clockTick);
}
function logEvent(lane, state, title, now) {
  if (lane.lastState === state) return;
  const old = lane.lastState;
  lane.lastState = state;
  // Avoid filling history with startup transitions. Retain interruptions and
  // their recovery; the original warning survives a successful reconnect.
  if (old === null && ['starting', 'recovering', 'unavailable'].includes(state)) return;
  const seconds = ((now - beginning) / 1000).toFixed(1);
  events.unshift({seconds, picture: labels[lane.route], title});
  if (events.length > 12) events.length = 12;
  const list = $('events');
  list.replaceChildren(...events.map(item => {
    const li = document.createElement('li');
    li.textContent = item.seconds + ' s — ' + item.picture + ': ' + item.title;
    return li;
  }));
  if (['stalled', 'ended', 'paused', 'hidden', 'unavailable'].includes(state)) {
    lane.lastInterruption = seconds + ' s: ' + title;
    text(lane.route + '-last-event', 'Last interruption at ' + lane.lastInterruption);
  }
}
function statsSnapshot(report) {
  const video = [...report.values()].filter(s =>
    s.type === 'inbound-rtp' && !s.isRemote && (s.kind === 'video' || s.mediaType === 'video'));
  if (video.length !== 1) return null;
  const s = video[0];
  if (typeof s.id !== 'string' || s.id.length > 256 ||
      !finite(s.timestamp) || !Number.isSafeInteger(s.framesDecoded) ||
      !Number.isSafeInteger(s.bytesReceived)) return null;
  return {id: s.id, timestamp: s.timestamp, framesDecoded: s.framesDecoded, bytesReceived: s.bytesReceived};
}
function beginStatsRead(lane, now) {
  if (lane.statsPending || now - lane.lastStatsStart < 500 || !lane.receiver?.getStats) return;
  lane.lastStatsStart = now;
  lane.statsPending = true;
  const generation = lane.generation, receiver = lane.receiver;
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    if (lane.generation === generation && !closed) {
      lane.lastStats = null;
      lane.watch.stats(performance.now(), null);
    }
  }, 800);
  // Never start another read until this one settles, even after its timeout.
  // A broken browser promise cannot create an unbounded queue of requests.
  Promise.resolve().then(() => receiver.getStats()).then(report => {
    if (closed || generation !== lane.generation || receiver !== lane.receiver || timedOut) return;
    lane.lastStats = statsSnapshot(report);
    lane.watch.stats(performance.now(), lane.lastStats);
  }).catch(() => {
    if (!closed && generation === lane.generation) {
      lane.lastStats = null;
      lane.watch.stats(performance.now(), null);
    }
  }).finally(() => {
    clearTimeout(timer);
    lane.statsPending = false;
  });
}
function watchFrames(lane) {
  if (!lane.supported) return;
  const generation = lane.generation;
  const next = (_callbackNow, metadata) => {
    if (closed || generation !== lane.generation) return;
    // Use one monotonic clock at observation time for all inputs. The callback
    // timestamp and mediaTime are not camera capture time.
    lane.watch.frame(performance.now(), metadata.mediaTime, metadata.presentedFrames);
    lane.callbackCount++;
    lane.callbackID = lane.video.requestVideoFrameCallback(next);
  };
  lane.callbackID = lane.video.requestVideoFrameCallback(next);
}
function stopLane(lane) {
  lane.generation++;
  if (lane.callbackID !== null && lane.video.cancelVideoFrameCallback) {
    lane.video.cancelVideoFrameCallback(lane.callbackID);
  }
  lane.callbackID = null;
  lane.reader?.close();
  lane.reader = null;
  lane.stream?.getTracks().forEach(track => track.stop());
  lane.receiver = null;
}
function reconnect(lane) {
  stopLane(lane);
  lane.watch.reset(performance.now());
  lane.stream = new MediaStream();
  lane.video.srcObject = lane.stream;
  lane.video.muted = true;
  lane.failed = false;
  lane.ended = false;
  lane.lastStats = null;
  lane.lastStatsStart = -Infinity;
  lane.callbackCount = 0;
  lane.bufferControl = 'browser default';
  lane.reconnectUntil = performance.now() + 1500;
  const generation = lane.generation;
  watchFrames(lane);
  try {
    lane.reader = new MediaMTXWebRTCReader({
      url: new URL('/whep/' + lane.route + '/' + encodeURIComponent(lane.source), location.href).href,
      onError: () => {
        if (closed || generation !== lane.generation) return;
        lane.failed = true;
        lane.watch.reset(performance.now());
      },
      onTrack: event => {
        if (closed || generation !== lane.generation || event.track.kind !== 'video') return;
        if (lane.receiver && lane.receiver !== event.receiver) {
          lane.stream.getTracks().forEach(track => { lane.stream.removeTrack(track); track.stop(); });
        }
        lane.receiver = event.receiver;
        lane.failed = false;
        lane.ended = false;
        lane.watch.reset(performance.now());
        lane.lastStats = null;
        // Preserve the existing clock players' browser-default buffering.
        // Playback observations must not change the setting being measured.
        if (!lane.stream.getTracks().includes(event.track)) lane.stream.addTrack(event.track);
        event.track.addEventListener('ended', () => {
          if (generation === lane.generation && lane.receiver?.track === event.track) lane.ended = true;
        });
        lane.video.play().catch(() => { /* Native play control remains available. */ });
      },
    });
  } catch {
    lane.failed = true;
  }
}
function render(now) {
  const diagnostics = {cameraAgeVerified: false, visibility: document.visibilityState, pictures: {}};
  for (const lane of lanes) {
    const dtls = lane.receiver?.transport?.state ?? 'unavailable';
    const connected = !lane.failed && dtls === 'connected';
    const state = lane.watch.inspect(now, {
      visible: document.visibilityState === 'visible',
      supported: lane.supported,
      paused: lane.receiver !== null && lane.video.paused,
      ended: lane.ended || lane.video.ended,
      connected,
      observationAvailable: !lane.video.error && lane.video.srcObject === lane.stream,
    });
    lane.result = state;
    $(lane.route + '-panel').dataset.state = state.state;
    text(lane.route + '-state', state.title);
    let detail = state.detail;
    if (state.state === 'stalled' && state.frameAgeMS !== null) {
      detail = 'No new picture timestamp for ' + (state.frameAgeMS / 1000).toFixed(1) + ' seconds. ' + detail;
    }
    text(lane.route + '-detail', detail);
    const connection = lane.failed ? 'unavailable' : connected ? 'open' : dtls;
    text(lane.route + '-connection', 'Connection: ' + connection + ' · camera age not verified');
    $(lane.route + '-reconnect').disabled = now < lane.reconnectUntil;
    logEvent(lane, state.state, state.title, now);
    diagnostics.pictures[lane.route] = {
      state: state.state, evidence: state.evidence,
      millisecondsSincePresentedProgress: state.frameAgeMS,
      connection, presentationCallbacksSupported: lane.supported,
      callbacksObserved: lane.callbackCount,
      paused: lane.video.paused, width: lane.video.videoWidth, height: lane.video.videoHeight,
      bufferControl: lane.bufferControl,
      counters: lane.lastStats ? {
        framesDecoded: lane.lastStats.framesDecoded, bytesReceived: lane.lastStats.bytesReceived,
      } : null,
    };
    beginStatsRead(lane, now);
  }
  text('diagnostics', JSON.stringify(diagnostics, null, 2));
}
function poll() {
  if (closed) return;
  render(performance.now());
  pollTimer = setTimeout(poll, 250);
}
function start() {
  closed = false;
  for (const route of ['local', 'forwarded']) {
    const video = $(route + '-video');
    const lane = {route, source: document.querySelector('main').dataset.source, video,
      watch: createWatch(), generation: 0, callbackID: null, callbackCount: 0,
      supported: typeof video.requestVideoFrameCallback === 'function',
      reader: null, receiver: null, stream: null, lastState: null, lastInterruption: null};
    lanes.push(lane);
    $(route + '-reconnect').addEventListener('click', () => {
      if (!closed && performance.now() >= lane.reconnectUntil) reconnect(lane);
    });
    reconnect(lane);
  }
  clockTick();
  poll();
}
document.addEventListener('visibilitychange', () => {
  if (!closed && lanes.length) render(performance.now());
});
window.addEventListener('pagehide', () => {
  closed = true;
  clearTimeout(pollTimer);
  cancelAnimationFrame(clockRequest);
  lanes.forEach(stopLane);
});
window.addEventListener('pageshow', event => {
  if (event.persisted && closed) {
    closed = false;
    lanes.forEach(reconnect);
    clockTick();
    poll();
  }
});
if (document.readyState === 'complete') start();
else window.addEventListener('DOMContentLoaded', start, {once: true});
