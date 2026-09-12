/* TEST ONLY. Real in-page WebRTC peers and canvas video, never WHEP or a camera. */
(() => {
  'use strict';
  const sources = new Map();
  const ended = new Set();
  const markerSize = 296, historyLimit = 30;
  const opticalModes = new Set(['normal', 'delay', 'freeze', 'blank', 'wrong-session', 'two']);
  const modeLabels = {normal: 'current marker', delay: 'marker delayed by at least 1 s',
    freeze: 'marker frozen; source frames still advance', blank: 'marker absent',
    'wrong-session': 'wrong session', two: 'two readable markers'};
  let playerMessage = '';
  const markerCanvas = () => {
    const marker = document.getElementById('delay-marker');
    if (!marker || marker.hidden || !marker.isConnected || marker.width <= 0 || marker.height <= 0 ||
        marker.getClientRects().length === 0 || getComputedStyle(marker).visibility === 'hidden') return null;
    return marker;
  };
  const copyCanvas = (source, reuse) => {
    const canvas = reuse ?? document.createElement('canvas');
    if (canvas.width !== markerSize || canvas.height !== markerSize) canvas.width = canvas.height = markerSize;
    const context = canvas.getContext('2d', {alpha: false});
    if (!context) throw new Error('Fixture marker copying is unavailable');
    context.imageSmoothingEnabled = false;
    context.fillStyle = '#ffffff';
    context.fillRect(0, 0, markerSize, markerSize);
    context.drawImage(source, 0, 0, markerSize, markerSize);
    return canvas;
  };
  const wrongSessionCanvas = () => {
    const code = globalThis.QRCode?.create('FS1:000000000000000000000000:0', {errorCorrectionLevel: 'M'});
    if (!code?.modules || !Number.isInteger(code.modules.size) || code.modules.size > 177) throw new Error('Fixture QR encoder unavailable');
    const canvas = document.createElement('canvas');
    canvas.width = canvas.height = markerSize;
    const c = canvas.getContext('2d', {alpha: false});
    c.fillStyle = '#ffffff';
    c.fillRect(0, 0, markerSize, markerSize);
    const cells = code.modules.size, scale = Math.floor(markerSize / (cells + 8));
    const origin = Math.floor((markerSize - cells * scale) / 2);
    c.fillStyle = '#000000';
    for (let row = 0; row < cells; row++) for (let col = 0; col < cells; col++) {
      if (code.modules.get(row, col)) c.fillRect(origin + col * scale, origin + row * scale, scale, scale);
    }
    return canvas;
  };
  const status = () => {
    const node = document.getElementById('fixture-status');
    if (!node) return;
    node.textContent = ['local', 'forwarded'].map(route => {
      const source = sources.get(route);
      if (!source) return `${route}: ${ended.has(route) ? 'TEST source ended; use the production Reconnect button to create a new test source' : 'waiting for test source'}`;
      return `${route}: ${source.held ? 'FRAMES HELD' : 'drawing 20 frames/s'}; ${modeLabels[source.opticalMode]}; sender ${source.sender.connectionState}; receiver ${source.receiver.connectionState}`;
    }).join('\n') + (playerMessage ? '\n' + playerMessage : '');
  };

  class FixtureReader {
    constructor(config) {
      this.config = config;
      this.closed = false;
      this.held = false;
      this.cancellations = new Set();
      this.timer = null;
      this.frame = 0;
      this.opticalMode = 'normal';
      this.markerHistory = [];
      this.markerHead = 0;
      this.frozenMarker = null;
      this.wrongMarker = null;
      queueMicrotask(() => this.start().catch(() => {
        if (!this.closed) {
          this.close();
          this.config.onError?.('TEST ONLY: the isolated browser peer connection could not start.');
        }
      }));
    }

    async start() {
      if (this.closed) return;
      const target = new URL(this.config.url, location.href);
      const match = /^\/whep\/(local|forwarded)\/camera-01$/.exec(target.pathname);
      if (target.origin !== location.origin || target.search || target.hash || !match) throw new Error('Unexpected fixture route');
      this.route = match[1];
      this.canvas = document.createElement('canvas');
      this.canvas.width = 640;
      this.canvas.height = 360;
      this.context = this.canvas.getContext('2d', {alpha: false});
      if (!this.context || !this.canvas.captureStream) throw new Error('Canvas video is unavailable');
      this.stream = this.canvas.captureStream(0);
      this.track = this.stream.getVideoTracks()[0];
      if (typeof this.track?.requestFrame !== 'function') throw new Error('Manual canvas frames are unavailable');
      this.sender = new RTCPeerConnection({iceServers: []});
      this.receiver = new RTCPeerConnection({iceServers: []});
      this.sender.addEventListener('connectionstatechange', status);
      this.receiver.addEventListener('connectionstatechange', status);
      this.receiver.addEventListener('track', event => {
        if (!this.closed) this.config.onTrack?.(event);
      });
      this.sender.addTrack(this.track, this.stream);
      sources.set(this.route, this);
      ended.delete(this.route);
      status();
      this.draw();
      this.timer = setInterval(() => this.draw(), 50);
      await this.sender.setLocalDescription(await this.sender.createOffer());
      await this.gather(this.sender);
      if (this.closed) return;
      await this.receiver.setRemoteDescription(this.sender.localDescription);
      await this.receiver.setLocalDescription(await this.receiver.createAnswer());
      await this.gather(this.receiver);
      if (this.closed) return;
      await this.sender.setRemoteDescription(this.receiver.localDescription);
    }

    gather(peer) {
      if (peer.iceGatheringState === 'complete') return Promise.resolve();
      return new Promise((resolve, reject) => {
        let timeout;
        const cleanup = () => {
          clearTimeout(timeout);
          peer.removeEventListener('icegatheringstatechange', change);
          this.cancellations.delete(cancel);
        };
        const cancel = () => { cleanup(); reject(new Error('Fixture closed')); };
        const change = () => {
          if (peer.iceGatheringState === 'complete') { cleanup(); resolve(); }
        };
        this.cancellations.add(cancel);
        peer.addEventListener('icegatheringstatechange', change);
        timeout = setTimeout(() => { cleanup(); reject(new Error('Local ICE gathering timed out')); }, 5000);
        if (this.closed) cancel();
        else change();
      });
    }

    opticalImage(now) {
      const marker = markerCanvas();
      if (!marker) {
        this.clearOptical();
        return null;
      }
      if (this.route === 'local') return marker;
      // Reuse a fixed number of 296 x 296 canvases. No frames accumulate after
      // the ring reaches its limit, and it stores no full-size video frames.
      const entry = this.markerHistory[this.markerHead] ?? {canvas: null, at: 0};
      entry.canvas = copyCanvas(marker, entry.canvas);
      entry.at = now;
      this.markerHistory[this.markerHead] = entry;
      this.markerHead = (this.markerHead + 1) % historyLimit;
      switch (this.opticalMode) {
        case 'blank': return null;
        case 'wrong-session': return this.wrongMarker ??= wrongSessionCanvas();
        case 'freeze': return this.frozenMarker ??= copyCanvas(entry.canvas);
        case 'delay': {
          // Time-based selection prevents throttled timers from turning
          // twenty queued frames into an incorrectly labelled one-second delay.
          let selected = null;
          for (const item of this.markerHistory) {
            if (item.at <= now - 1000 && (!selected || item.at > selected.at)) selected = item;
          }
          return selected?.canvas ?? null;
        }
        default: return entry.canvas;
      }
    }

    clearOptical() {
      this.markerHistory.length = 0;
      this.markerHead = 0;
      this.frozenMarker = null;
      this.wrongMarker = null;
    }

    setOpticalMode(mode) {
      if (this.closed || this.route !== 'forwarded' || !opticalModes.has(mode)) return;
      this.opticalMode = mode;
      this.clearOptical();
      this.draw();
      status();
    }

    draw() {
      if (this.closed || this.held) return;
      const c = this.context;
      const now = performance.now();
      c.fillStyle = this.route === 'local' ? '#102438' : '#182c24';
      c.fillRect(0, 0, 640, 360);
      const optical = this.opticalImage(now);
      c.imageSmoothingEnabled = false;
      if (optical && this.opticalMode === 'two') {
        c.drawImage(optical, 24, 72, 220, 220);
        c.drawImage(optical, 336, 72, 220, 220);
      } else if (optical) c.drawImage(optical, 24, 32, markerSize, markerSize);
      c.fillStyle = '#ffffff';
      c.font = 'bold 23px monospace';
      if (this.opticalMode === 'two') {
        c.fillText('TEST ONLY / TWO MARKERS', 24, 40);
      } else {
        c.fillText(`TEST / ${this.route.toUpperCase()}`, 340, 60);
        c.font = 'bold 32px monospace';
        c.fillText((now / 1000).toFixed(2), 340, 120);
        c.font = '17px monospace';
        c.fillText('GENERATED SOURCE', 340, 162);
        c.fillText('640 x 360 / 20 fps', 340, 194);
        if (!optical) c.fillText('No test QR available', 340, 225);
      }
      c.fillStyle = '#ffd43b';
      c.fillRect(340 + (this.frame * 8) % 220, 304, 40, 24);
      this.frame += 1;
      this.track.requestFrame();
    }

    hold(value) {
      if (this.closed) return;
      this.held = value;
      if (!value) this.draw();
      status();
    }

    end() {
      if (this.closed) return;
      ended.add(this.route);
      this.close();
      this.config.onError?.('TEST ONLY: the forwarded test connection was ended.');
    }

    close() {
      if (this.closed) return;
      this.closed = true;
      clearInterval(this.timer);
      this.clearOptical();
      for (const cancel of [...this.cancellations]) cancel();
      this.stream?.getTracks().forEach(track => track.stop());
      this.receiver?.getReceivers().forEach(receiver => receiver.track?.stop());
      this.sender?.close();
      this.receiver?.close();
      if (sources.get(this.route) === this) sources.delete(this.route);
      status();
    }
  }

  globalThis.MediaMTXWebRTCReader = FixtureReader;
  const setup = () => {
    const bind = (id, callback) => document.getElementById(id)?.addEventListener('click', callback);
    bind('fixture-hold-forwarded', () => sources.get('forwarded')?.hold(true));
    bind('fixture-resume-forwarded', () => sources.get('forwarded')?.hold(false));
    bind('fixture-hold-both', () => { for (const source of sources.values()) source.hold(true); });
    bind('fixture-resume-both', () => { for (const source of sources.values()) source.hold(false); });
    bind('fixture-end-forwarded', () => sources.get('forwarded')?.end());
    for (const mode of opticalModes) bind('fixture-age-' + mode, () => sources.get('forwarded')?.setOpticalMode(mode));
    bind('fixture-pause-forwarded-player', () => {
      const video = document.getElementById('forwarded-video');
      if (!video) return;
      video.pause();
      playerMessage = 'TEST: forwarded player paused; the canvas source keeps sending.';
      status();
    });
    bind('fixture-play-forwarded-player', async () => {
      const video = document.getElementById('forwarded-video');
      const button = document.getElementById('fixture-play-forwarded-player');
      if (!video || button.disabled) return;
      button.disabled = true;
      try {
        await video.play();
        playerMessage = 'TEST: forwarded player resumed.';
      } catch {
        playerMessage = 'TEST: the browser could not resume the forwarded player. Use its native play control.';
      } finally {
        button.disabled = false;
        status();
      }
    });
    status();
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', setup, {once: true});
  else setup();
  addEventListener('pagehide', () => { for (const source of [...sources.values()]) source.close(); });
  addEventListener('beforeunload', () => { for (const source of [...sources.values()]) source.close(); });
})();
