/* TEST ONLY. Real in-page WebRTC peers and canvas video, never WHEP or a camera. */
(() => {
  'use strict';
  const sources = new Map();
  const ended = new Set();
  let playerMessage = '';
  const status = () => {
    const node = document.getElementById('fixture-status');
    if (!node) return;
    node.textContent = ['local', 'forwarded'].map(route => {
      const source = sources.get(route);
      if (!source) return `${route}: ${ended.has(route) ? 'TEST source ended; use the production Reconnect button to create a new test source' : 'waiting for test source'}`;
      return `${route}: ${source.held ? 'FRAMES HELD' : 'drawing 20 frames/s'}; sender ${source.sender.connectionState}; receiver ${source.receiver.connectionState}`;
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

    draw() {
      if (this.closed || this.held) return;
      const c = this.context;
      c.fillStyle = this.route === 'local' ? '#102438' : '#182c24';
      c.fillRect(0, 0, 640, 360);
      c.fillStyle = '#ffffff';
      c.font = 'bold 28px monospace';
      c.fillText(`TEST ONLY / ${this.route.toUpperCase()}`, 24, 48);
      c.font = 'bold 56px monospace';
      c.fillText((performance.now() / 1000).toFixed(2).padStart(8, '0'), 24, 126);
      c.font = '20px monospace';
      c.fillText('GENERATED BROWSER SOURCE', 24, 170);
      c.fillText('640 x 360 / manual 20 fps', 24, 205);
      c.fillStyle = '#ffd43b';
      c.fillRect(24 + (this.frame * 8) % 532, 250, 60, 60);
      c.fillStyle = '#ffffff';
      c.fillRect(24, 330, 590, 2);
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
