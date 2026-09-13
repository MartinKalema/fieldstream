"use strict";

const clock = document.getElementById("clock");
const started = performance.now();

function drawClock() {
  clock.textContent = ((performance.now() - started) / 1000).toFixed(2).padStart(7, "0");
  requestAnimationFrame(drawClock);
}

drawClock();
