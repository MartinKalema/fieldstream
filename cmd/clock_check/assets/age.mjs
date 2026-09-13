// Optical calibration only: the camera must film the marker displayed by this
// page. These intervals describe the marker's display time, not verified sensor
// capture time. They include marker hold time but exclude screen/exposure error.
const clockValue = value => typeof value === 'number' && Number.isFinite(value) &&
  value >= 0 && value <= Number.MAX_SAFE_INTEGER;
const sessionPattern = /^[0-9A-F]{24}$/;
const payloadPattern = /^FS1:([0-9A-F]{24}):(0|[1-9][0-9]{0,15})$/;
const laneNames = ['local', 'forwarded'];
const markerLimit = 2048;

function validSession(value) {
  return typeof value === 'string' && value.length === 24 && sessionPattern.test(value);
}

function parsePayload(payload) {
  if (typeof payload !== 'string' || payload.length > 45) return null;
  const match = payloadPattern.exec(payload);
  // JavaScript's $ can match before a final newline. Require the whole input.
  if (!match || match[0].length !== payload.length) return null;
  const sequence = Number(match[2]);
  if (!Number.isSafeInteger(sequence)) return null;
  return {session: match[1], sequence};
}

function makeLane() {
  return {status: 'waiting', current: null, invalidatedAt: null, lastSampledAt: null,
    attempted: 0, values: [], medianMidpointMS: null, maxObservedUpperMS: null, sampleLimitReached: false};
}

export function createAge({durationMS = 120000, maxResultDelayMS = 250,
  currentTTLMS = 1000, maxSamplesPerLane = 512} = {}) {
  for (const [value, maximum] of [[durationMS, 120000], [maxResultDelayMS, 250], [currentTTLMS, 1000]]) {
    if (!clockValue(value) || value <= 0 || value > maximum) throw new RangeError('Calibration intervals exceed their bounded limits.');
  }
  if (!Number.isSafeInteger(maxSamplesPerLane) || maxSamplesPerLane < 1 || maxSamplesPerLane > 512)
    throw new RangeError('Calibration allows from 1 to 512 attempts per picture.');

  let active = false, session = null, startedAt = null, expiresAt = null, endedAt = null;
  let stopReason = 'not-started', lastNow = null, lastEmission = null;
  let markers = new Map();
  let lanes = Object.fromEntries(laneNames.map(name => [name, makeLane()]));

  function getLane(name) {
    if (!laneNames.includes(name)) throw new RangeError('Unknown calibration picture.');
    return lanes[name];
  }

  function finish(now, reason) {
    active = false;
    endedAt = now;
    stopReason = reason;
    markers.clear();
    lastEmission = null;
    for (const lane of Object.values(lanes)) {
      lane.current = null;
      lane.status = 'finished';
    }
  }

  function observe(now) {
    if (!active) return false;
    if (!clockValue(now) || now < lastNow) {
      finish(null, 'clock-invalid');
      return false;
    }
    lastNow = now;
    if (now >= expiresAt) {
      finish(expiresAt, 'time-limit');
      return false;
    }
    for (const lane of Object.values(lanes)) {
      if (lane.current && now - lane.current.sampledAt >= currentTTLMS) {
        lane.current = null;
        lane.status = 'stale';
      }
    }
    return true;
  }

  function summary(lane) {
    return {attempted: lane.attempted, valid: lane.values.length, medianMidpointMS: lane.medianMidpointMS,
      maxObservedUpperMS: lane.maxObservedUpperMS, sampleLimitReached: lane.sampleLimitReached};
  }

  function snapshot() {
    return {active, session, startedAt, expiresAt, endedAt, stopReason,
      lanes: Object.fromEntries(laneNames.map(name => {
        const lane = lanes[name];
        return [name, {status: lane.status, current: lane.current ? {...lane.current} : null, summary: summary(lane)}];
      }))};
  }

  // Supply a fresh cryptographically random session for every run. The session
  // and sequence are optical correlation identifiers, never camera credentials.
  function start(now, nextSession) {
    if (!clockValue(now) || now + durationMS > Number.MAX_SAFE_INTEGER || !validSession(nextSession))
      throw new RangeError('Calibration needs a valid monotonic time and 24 uppercase hexadecimal session characters.');
    if (nextSession === session) throw new RangeError('Every calibration run needs a new session identifier.');
    active = true;
    session = nextSession;
    startedAt = lastNow = now;
    expiresAt = now + durationMS;
    endedAt = null;
    stopReason = null;
    markers = new Map();
    lastEmission = null;
    lanes = Object.fromEntries(laneNames.map(name => [name, makeLane()]));
    return snapshot();
  }

  // Call at the actual marker-render boundary, not when QR generation begins.
  // New emissions close the preceding marker's display interval. A still-open
  // interval has a conservative zero lower age bound when a reading arrives.
  function emit(now, sequence) {
    if (!observe(now)) return null;
    if (!Number.isSafeInteger(sequence) || sequence < 0 ||
        (lastEmission && (sequence <= lastEmission.sequence || now <= lastEmission.emittedAt))) {
      finish(now, 'invalid-emission');
      return null;
    }
    if (markers.size >= markerLimit) {
      finish(now, 'marker-limit');
      return null;
    }
    if (lastEmission) lastEmission.nextEmissionAt = now;
    lastEmission = {sequence, emittedAt: now, nextEmissionAt: null};
    markers.set(sequence, lastEmission);
    return `FS1:${session}:${sequence}`;
  }

  function fail(lane, status) {
    lane.current = null;
    lane.status = status;
    return snapshot();
  }

  // sampledAt is when the inspected image was copied from the video, observedAt
  // is when decoding completed. A worker reply must not replace a newer sample.
  function read(name, result = {}) {
    const lane = getLane(name);
    const {sampledAt, observedAt, status, payload} = result || {};
    if (!observe(observedAt)) return snapshot();
    if (lane.sampleLimitReached) return fail(lane, 'sample-limit');
    lane.attempted++;
    lane.sampleLimitReached = lane.attempted >= maxSamplesPerLane;
    lane.current = null;

    if (!clockValue(sampledAt) || sampledAt > observedAt) return fail(lane, 'invalid-sample-time');
    if (sampledAt < startedAt || (lane.invalidatedAt !== null && sampledAt < lane.invalidatedAt))
      return fail(lane, 'invalidated-sample');
    if (lane.lastSampledAt !== null && sampledAt <= lane.lastSampledAt) return fail(lane, 'out-of-order');
    lane.lastSampledAt = sampledAt;
    if (observedAt - sampledAt > maxResultDelayMS) return fail(lane, 'late-result');
    if (status !== 'decoded') return fail(lane, status === 'ambiguous' ? 'ambiguous' : 'unreadable');
    const parsed = parsePayload(payload);
    if (!parsed) return fail(lane, 'invalid-marker');
    if (parsed.session !== session) return fail(lane, 'wrong-session');
    const marker = markers.get(parsed.sequence);
    if (!marker) return fail(lane, 'unknown-sequence');
    if (marker.emittedAt > sampledAt) return fail(lane, 'future-marker');

    const lowerMS = marker.nextEmissionAt === null ? 0 : Math.max(0, sampledAt - marker.nextEmissionAt);
    const upperMS = sampledAt - marker.emittedAt;
    const midpointMS = (lowerMS + upperMS) / 2;
    lane.current = {lowerMS, upperMS, midpointMS, sequence: parsed.sequence, sampledAt, observedAt};
    lane.status = 'measured';
    lane.values.push(midpointMS);
    // RAF and presentation callbacks inspect frequently; sort only when a new
    // accepted measurement changes the summary, not on every observation.
    const sorted = [...lane.values].sort((a, b) => a - b);
    const middle = Math.floor(sorted.length / 2);
    lane.medianMidpointMS = sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2;
    lane.maxObservedUpperMS = lane.maxObservedUpperMS === null ? upperMS : Math.max(lane.maxObservedUpperMS, upperMS);
    return snapshot();
  }

  function invalidate(name, now, _reason) {
    const lane = getLane(name);
    if (!observe(now)) return snapshot();
    lane.invalidatedAt = now;
    return fail(lane, 'invalidated');
  }

  function inspect(now) {
    observe(now);
    return snapshot();
  }

  function stop(now) {
    if (observe(now)) finish(now, 'stopped');
    return snapshot();
  }

  return {start, emit, read, invalidate, inspect, stop};
}
