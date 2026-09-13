// All measurements here come from the page's real receiver, never a DOM proxy.
export const finite = value => typeof value === 'number' && Number.isFinite(value);
const fields = ['timestamp', 'jitterBufferDelay', 'jitterBufferTargetDelay',
  'jitterBufferMinimumDelay', 'jitterBufferEmittedCount', 'framesDecoded',
  'framesDropped', 'framesReceived', 'totalDecodeTime', 'freezeCount',
  'totalFreezesDuration', 'packetsReceived', 'packetsLost', 'nackCount',
  'pliCount', 'bytesReceived', 'jitter', 'framesPerSecond', 'frameWidth', 'frameHeight'];

export function videoSnapshot(report) {
  const candidates = [...report.values()].filter(s => s.type === 'inbound-rtp' &&
    (s.kind === 'video' || s.mediaType === 'video') && !s.isRemote && finite(s.framesDecoded));
  if (candidates.length !== 1) return null;
  const source = candidates[0], result = {id: source.id};
  for (const key of fields) if (finite(source[key])) result[key] = source[key];
  return result;
}

export function publicSnapshot(snapshot) {
  if (!snapshot) return null;
  const result = {};
  for (const key of fields) if (finite(snapshot[key])) result[key] = snapshot[key];
  return result;
}

export function bufferControl(receiver, zero) {
  const supported = {
    jitterBufferTarget: 'jitterBufferTarget' in receiver,
    playoutDelayHint: 'playoutDelayHint' in receiver,
    getStats: typeof receiver.getStats === 'function',
  };
  const property = supported.jitterBufferTarget ? 'jitterBufferTarget' :
    supported.playoutDelayHint ? 'playoutDelayHint' : null;
  const result = {supported, property, requested: zero ? 0 : null,
    unit: property === 'playoutDelayHint' ? 'seconds' : 'milliseconds',
    status: zero ? 'unsupported' : 'browser-default'};
  if (!property) return result;
  try {
    result.before = receiver[property] ?? null;
    if (zero) receiver[property] = 0;
    result.readback = receiver[property] ?? null;
    if (zero) result.status = result.readback === 0 ? 'request-accepted' : 'readback-mismatch';
  } catch (error) {
    result.status = 'error';
    result.errorName = error?.name || 'Error';
  }
  return result;
}

export function delta(a, b, key) {
  if (!finite(a?.[key]) || !finite(b?.[key])) return null;
  const value = b[key] - a[key];
  return value >= 0 ? value : null;
}

export function interval(a, b) {
  if (!a || !b || a.id !== b.id) return {valid: false, reason: 'Receiver or statistics stream changed.'};
  const elapsedMS = delta(a, b, 'timestamp');
  const emitted = delta(a, b, 'jitterBufferEmittedCount');
  const decoded = delta(a, b, 'framesDecoded');
  if (!elapsedMS || elapsedMS > 2500 || emitted === null || decoded === null)
    return {valid: false, reason: 'Counter reset, unavailable counter, or sampling pause.'};
  const result = {valid: true, elapsedMS, emitted, decoded};
  for (const key of ['jitterBufferDelay', 'jitterBufferTargetDelay', 'jitterBufferMinimumDelay',
    'totalDecodeTime', 'framesDropped', 'freezeCount', 'totalFreezesDuration',
    'packetsReceived', 'packetsLost', 'nackCount', 'pliCount', 'bytesReceived']) result[key] = delta(a, b, key);
  if (emitted === 0 || decoded === 0) return {...result, valid: false, reason: 'Video decoded/emitted counters did not both advance.'};
  if (result.jitterBufferDelay === null) return {valid: false, reason: 'Buffer delay is unavailable or reset.'};
  for (const key of ['jitterBufferDelay', 'jitterBufferTargetDelay', 'jitterBufferMinimumDelay'])
    result[key + 'MS'] = emitted > 0 && result[key] !== null ? 1000 * result[key] / emitted : null;
  result.decodeMS = decoded > 0 && result.totalDecodeTime !== null ? 1000 * result.totalDecodeTime / decoded : null;
  return result;
}

export function summary(samples) {
  const valid = samples.filter(s => s.valid);
  const sum = key => valid.length && valid.every(s => finite(s[key])) ? valid.reduce((a, s) => a + s[key], 0) : null;
  const emitted = sum('emitted'), decoded = sum('decoded');
  const result = {validIntervals: valid.length, invalidIntervals: samples.length - valid.length,
    sampledSeconds: sum('elapsedMS') === null ? null : sum('elapsedMS') / 1000,
    emittedFrames: emitted, decodedFrames: decoded};
  for (const key of ['jitterBufferDelay', 'jitterBufferTargetDelay', 'jitterBufferMinimumDelay'])
    result[key + 'MS'] = emitted > 0 && sum(key) !== null ? 1000 * sum(key) / emitted : null;
  result.decodeMS = decoded > 0 && sum('totalDecodeTime') !== null ? 1000 * sum('totalDecodeTime') / decoded : null;
  for (const key of ['framesDropped', 'freezeCount', 'totalFreezesDuration', 'packetsLost', 'nackCount', 'pliCount']) result[key] = sum(key);
  return result;
}

export function frameTiming(metadata) {
  const receiveToDisplayMS = finite(metadata.receiveTime) && finite(metadata.expectedDisplayTime) &&
    metadata.receiveTime > 0 && metadata.expectedDisplayTime >= metadata.receiveTime ?
    metadata.expectedDisplayTime - metadata.receiveTime : null;
  return {receiveToExpectedDisplayMS: receiveToDisplayMS,
    decodeProcessingMS: finite(metadata.processingDuration) && metadata.processingDuration >= 0 ? metadata.processingDuration * 1000 : null,
    captureTimeExposed: finite(metadata.captureTime), receiveTimeExposed: finite(metadata.receiveTime)};
}
