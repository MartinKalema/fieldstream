// Decoding stays in this worker. No decoded content is opened or requested.
// The page owns one pending scan and terminates a worker that exceeds its budget.
'use strict';
importScripts('/vendor/jsQR.js');

const MAX_WIDTH = 960;
const MAX_HEIGHT = 540;
const MAX_PAYLOAD = 128;

self.onmessage = event => {
  const value = event.data;
  const id = Number.isSafeInteger(value?.id) && value.id >= 0 ? value.id : null;
  const unreadable = () => self.postMessage({id, status: 'unreadable'});
  if (id === null || !Number.isInteger(value.width) || !Number.isInteger(value.height) ||
      value.width < 1 || value.height < 1 || value.width > MAX_WIDTH || value.height > MAX_HEIGHT ||
      !(value.buffer instanceof ArrayBuffer) || value.buffer.byteLength !== value.width * value.height * 4) {
    unreadable();
    return;
  }
  const {width, height} = value;
  try {
    const pixels = new Uint8ClampedArray(value.buffer);
    const first = jsQR(pixels, width, height, {inversionAttempts: 'attemptBoth'});
    if (!first || typeof first.data !== 'string' || first.data.length === 0 || first.data.length > MAX_PAYLOAD) {
      unreadable();
      return;
    }
    const corners = ['topLeftCorner', 'topRightCorner', 'bottomLeftCorner', 'bottomRightCorner'].map(key => first.location?.[key]);
    if (corners.some(point => !point || !Number.isFinite(point.x) || !Number.isFinite(point.y))) {
      unreadable();
      return;
    }
    // Mask the decoded symbol's complete bounding rectangle, including a small
    // edge margin, then make just one additional scan for a second readable QR.
    const left = Math.max(0, Math.floor(Math.min(...corners.map(point => point.x))) - 3);
    const right = Math.min(width, Math.ceil(Math.max(...corners.map(point => point.x))) + 3);
    const top = Math.max(0, Math.floor(Math.min(...corners.map(point => point.y))) - 3);
    const bottom = Math.min(height, Math.ceil(Math.max(...corners.map(point => point.y))) + 3);
    if (right <= left || bottom <= top) {
      unreadable();
      return;
    }
    for (let y = top; y < bottom; y++) {
      const start = (y * width + left) * 4;
      pixels.fill(255, start, (y * width + right) * 4);
    }
    const second = jsQR(pixels, width, height, {inversionAttempts: 'attemptBoth'});
    if (second) self.postMessage({id, status: 'ambiguous'});
    else self.postMessage({id, status: 'read', payload: first.data});
  } catch {
    unreadable();
  }
};
