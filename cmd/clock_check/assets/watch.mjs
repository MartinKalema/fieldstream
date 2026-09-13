// This watches browser presentation, not the age or contents of camera pictures.
// Every `now` belongs to the same monotonic performance.now() clock. Metadata
// mediaTime and statistics timestamp have their own clocks and are never
// subtracted from `now`. Storage stays constant regardless of the run length.
const finite = value => typeof value === 'number' && Number.isFinite(value);
const nonnegative = value => finite(value) && value >= 0;
const counter = value => Number.isSafeInteger(value) && value >= 0;

const messages = {
  starting: ['Waiting for picture progress', 'Waiting for advancing video timestamps from the browser. A connected service alone does not prove that pictures are advancing.'],
  advancing: ['Picture advancing', 'The browser is presenting advancing video timestamps. The camera picture age is not verified.'],
  recovering: ['Checking picture recovery', 'Waiting for a sustained run of advancing video timestamps. The camera picture age is not verified.'],
  hidden: ['Picture check paused', 'This page is hidden. Picture progress will be checked again when the page is visible.'],
  paused: ['Playback paused', 'Playback is paused. Picture progress will be checked again after playback resumes.'],
  ended: ['Video has ended', 'Playback has ended. A new connection needs fresh picture observations.'],
};
const unavailableMessages = {
  unsupported: 'This browser does not provide the video presentation callbacks needed to check picture progress.',
  observation: 'Browser presentation cannot currently be observed. Waiting for fresh picture observations.',
  disconnected: 'The video connection is not connected. Waiting for fresh picture observations.',
  time: 'The observation clock was invalid or moved backwards. Waiting for fresh picture observations.',
};
const stalledDetails = {
  'decoded-frames-advancing': 'The browser is not presenting advancing video timestamps, although decoded-frame counters are increasing. The camera picture age is not verified.',
  'network-bytes-advancing': 'The browser is not presenting advancing video timestamps, although received-byte counters are increasing. Receiving bytes alone does not prove the picture is advancing.',
  'no-counter-progress': 'The browser is not presenting advancing video timestamps, and the latest receiver counters did not advance.',
  unavailable: 'The browser is not presenting advancing video timestamps. Receiver counters are unavailable.',
};

export function createWatch({stallMS = 1500, recoverMS = 1000, observationGapMS = 2000} = {}) {
  for (const value of [stallMS, recoverMS, observationGapMS]) {
    if (!finite(value) || value <= 0) throw new RangeError('Watch intervals must be finite positive milliseconds.');
  }

  let lastNow = null, epochAt = null, timeFault = false, gate = null;
  let frameBaseline = null, lastPresentedAt = null, lastProgressAt = null, runStartedAt = null;
  let needsRecovery = false;
  let identityStats = null, previousStats = null, latestEvidence = 'unavailable', evidenceAt = null;

  function clearCounters() {
    identityStats = previousStats = null;
    latestEvidence = 'unavailable';
    evidenceAt = null;
  }

  function clearPictures(now, recovering = true) {
    epochAt = now;
    frameBaseline = null;
    lastPresentedAt = lastProgressAt = runStartedAt = null;
    needsRecovery = recovering;
  }

  function clearContinuity(now, recovering = true) {
    clearPictures(now, recovering);
    clearCounters();
  }

  function reset(now) {
    lastNow = nonnegative(now) ? now : null;
    timeFault = lastNow === null;
    gate = null;
    clearContinuity(lastNow, timeFault);
  }

  // A long scheduling gap invalidates continuity; elapsed time without an
  // observer cannot prove either uninterrupted playback or recovery.
  function observe(now) {
    if (!nonnegative(now) || (lastNow !== null && now < lastNow)) {
      lastNow = nonnegative(now) ? now : null;
      clearContinuity(lastNow);
      timeFault = true;
      return false;
    }
    if (lastNow === null || timeFault) {
      clearContinuity(now, timeFault);
    } else if (now - lastNow > observationGapMS) {
      clearContinuity(now);
    }
    lastNow = now;
    timeFault = false;
    return true;
  }

  function frame(now, mediaTime, presentedFrames) {
    if (!observe(now) || gate !== null || !nonnegative(mediaTime) ||
        !counter(presentedFrames) || presentedFrames === 0) return;

    if (frameBaseline && (mediaTime < frameBaseline.mediaTime || presentedFrames < frameBaseline.presentedFrames)) {
      // A seek, track change, or frame-counter reset starts a new sequence.
      clearContinuity(now);
    }
    if (frameBaseline === null) {
      frameBaseline = {mediaTime, presentedFrames};
      lastPresentedAt = now;
      return;
    }

    const progressed = mediaTime > frameBaseline.mediaTime && presentedFrames > frameBaseline.presentedFrames;
    // Remember each high-water mark, including malformed one-sided advances.
    // A later callback must advance both values in that same observation.
    frameBaseline = {mediaTime, presentedFrames};
    if (!progressed) return;

    if (now - lastPresentedAt >= stallMS) {
      runStartedAt = null;
      needsRecovery = true;
    }
    if (runStartedAt === null) runStartedAt = now;
    lastPresentedAt = lastProgressAt = now;
  }

  // Pass null for a missing report, a timeout, or a caught getStats error.
  // Losing counters removes counter evidence, but valid frame observations
  // remain useful and do not depend on getStats being supported.
  function stats(now, snapshot) {
    if (!observe(now) || gate !== null) return;
    const valid = snapshot && typeof snapshot.id === 'string' && snapshot.id.length > 0 &&
      nonnegative(snapshot.timestamp) && counter(snapshot.bytesReceived) && counter(snapshot.framesDecoded);
    if (!valid) {
      previousStats = null;
      latestEvidence = 'unavailable';
      evidenceAt = null;
      return;
    }
    // Do not retain caller-owned objects or any unrelated fields.
    const current = {id: snapshot.id, timestamp: snapshot.timestamp,
      bytesReceived: snapshot.bytesReceived, framesDecoded: snapshot.framesDecoded, receivedAt: now};
    const resetDetected = identityStats && (current.id !== identityStats.id ||
      current.timestamp < identityStats.timestamp || current.bytesReceived < identityStats.bytesReceived ||
      current.framesDecoded < identityStats.framesDecoded);
    if (resetDetected) clearContinuity(now);

    latestEvidence = 'unavailable';
    evidenceAt = null;
    if (previousStats && now - previousStats.receivedAt <= observationGapMS &&
        current.timestamp > previousStats.timestamp) {
      latestEvidence = current.framesDecoded > previousStats.framesDecoded ? 'decoded-frames-advancing' :
        current.bytesReceived > previousStats.bytesReceived ? 'network-bytes-advancing' : 'no-counter-progress';
      evidenceAt = now;
    }
    identityStats = previousStats = current;
  }

  function inspect(now, {visible = true, supported = true, paused = false, ended = false,
    connected = false, observationAvailable = true} = {}) {
    if (!observe(now)) return {state: 'unavailable', title: 'Picture check unavailable',
      detail: unavailableMessages.time, frameAgeMS: null, evidence: 'unavailable'};

    const nextGate = !visible ? 'hidden' : !supported ? 'unsupported' : !observationAvailable ? 'observation' :
      ended ? 'ended' : paused ? 'paused' : !connected ? 'disconnected' : null;
    if (nextGate !== gate) {
      clearContinuity(now);
      gate = nextGate;
    }
    if (gate !== null) {
      const state = messages[gate] ? gate : 'unavailable';
      const [title, detail] = messages[gate] || ['Picture check unavailable', unavailableMessages[gate]];
      return {state, title, detail, frameAgeMS: null, evidence: 'unavailable'};
    }

    const frameAgeMS = lastPresentedAt === null ? null : now - lastPresentedAt;
    const evidence = evidenceAt !== null && now - evidenceAt <= observationGapMS ? latestEvidence : 'unavailable';
    let state;
    if (now - (lastPresentedAt ?? epochAt) >= stallMS) {
      state = 'stalled';
      needsRecovery = true;
      runStartedAt = null;
    } else if (runStartedAt !== null && lastProgressAt - runStartedAt >= recoverMS) {
      state = 'advancing';
      needsRecovery = false;
    } else {
      state = needsRecovery || runStartedAt !== null ? 'recovering' : 'starting';
    }
    const [title, detail] = state === 'stalled' ? ['Picture is not advancing', stalledDetails[evidence]] : messages[state];
    return {state, title, detail, frameAgeMS, evidence};
  }

  return {reset, frame, stats, inspect};
}
