const finite = value => typeof value === 'number' && Number.isFinite(value);
const positive = value => finite(value) && value > 0;
const allowedFiles = new Map([
  ['detail-20.mp4', {url: '/detail-20.mp4', label: 'More detail · 20 fps'}],
  ['small-20.mp4', {url: '/small-20.mp4', label: 'Smaller picture · 20 fps'}],
]);
const allowedScenes = new Set(['fast-motion', 'fine-detail', 'dim-noise']);

export function reportProvenance(report) {
  if (!report || typeof report !== 'object' || Array.isArray(report)) throw new Error('report shape');
  if (!Object.prototype.hasOwnProperty.call(report, 'test_scene')) return {kind: 'recording'};
  const scene = report.test_scene;
  if (!scene || typeof scene !== 'object' || Array.isArray(scene) || !allowedScenes.has(scene.id) ||
      typeof scene.title !== 'string' || !scene.title.trim() || scene.title.length > 160 ||
      typeof scene.note !== 'string' || !scene.note.trim() || scene.note.length > 2000) {
    throw new Error('invalid test scene provenance');
  }
  return {kind: 'generated', id: scene.id, title: scene.title, note: scene.note};
}

export function mediaChoice(variant) {
  if (!variant || typeof variant.file !== 'string') return null;
  const file = variant.file.startsWith('/') ? variant.file.slice(1) : variant.file;
  const choice = allowedFiles.get(file);
  return choice ? {...choice, file} : null;
}

export function fileSize(bytes) {
  return finite(bytes) && bytes >= 0 ? (bytes / 1_000_000).toFixed(2) + ' MB' : 'Unavailable';
}

export function sizeChange(originalBytes, encodedBytes) {
  if (!positive(originalBytes) || !finite(encodedBytes) || encodedBytes < 0) return 'Unavailable';
  const percent = 100 * (originalBytes - encodedBytes) / originalBytes;
  if (Math.abs(percent) < 0.05) return 'About the same';
  return Math.abs(percent).toFixed(1) + (percent > 0 ? '% smaller' : '% larger');
}

export function similarity(value) {
  return finite(value) && value >= -1 && value <= 1 ? value.toFixed(4) : 'Unavailable';
}

export function clampTime(value, duration) {
  if (!finite(value) || !positive(duration)) return 0;
  return Math.max(0, Math.min(value, duration));
}

const dimension = item => positive(item?.width) && positive(item?.height) ? `${item.width} × ${item.height}` : 'Unavailable';
const frameRate = value => positive(value) ? `${Number(value.toFixed(2))} fps` : 'Unavailable';
const seconds = value => finite(value) && value >= 0 ? value.toFixed(2) + ' s' : 'Unavailable';
const count = value => Number.isInteger(value) && value >= 0 ? value.toLocaleString('en') : 'Unavailable';

export function initializePage(document) {
  const $ = id => document.getElementById(id);
  const original = $('original'), encoded = $('encoded');
  let report = null, choices = [], selected = null, sharedDuration = 0, generation = 0;
  original.muted = encoded.muted = true;

  function status(message, error = false) {
    $('status').textContent = message;
    $('status').classList.toggle('error', error);
  }

  function showProvenance(provenance) {
    const generated = provenance.kind === 'generated';
    document.title = generated ? 'Generated scene comparison' : 'Recorded video comparison';
    $('provenance-label').textContent = generated ? 'Generated test scene' : 'Local recording experiment';
    $('page-title').textContent = generated ? provenance.title : 'How much smaller can this video be?';
    $('introduction').textContent = generated
      ? 'Compare this generated source with two versions made at 20 frames per second. Look at small details and movement, then compare the file size and the work needed to create each version.'
      : 'Compare the original recording with two versions made at 20 frames per second. Look at small details and movement, then compare the file size and the work needed to create each version.';
    $('provenance-notice').textContent = generated
      ? 'This footage was generated on this computer. Results describe this test scene. This page does not change the current live feed, camera settings or saved camera recordings.'
      : 'This page plays saved test files. It does not change the current live feed, camera settings or original recording.';
    $('scene-note').textContent = generated ? provenance.note : '';
    $('scene-note').hidden = !generated;
    $('original-title').textContent = generated ? 'Generated source picture' : 'Original picture';
    original.setAttribute('aria-label', generated ? 'Generated source picture with playback starting at zero' : 'Original picture with playback starting at zero');
    encoded.setAttribute('aria-label', generated ? 'Selected encoded test scene' : 'Selected encoded recording');
    $('source-copy-note').textContent = generated
      ? 'The generated source picture plays from a separate copy whose timeline starts at zero. Its compressed video frames are unchanged, but its file bytes differ. The generated source file stays unchanged; the original file size and checksum shown on this page describe that source file.'
      : 'The original picture plays from a separate copy whose timeline starts at zero. Its compressed video frames are unchanged, but its file bytes differ. The source recording stays unchanged; the original file size and checksum shown on this page describe that source recording.';
  }

  function addCell(row, text, detail = '', heading = false) {
    const cell = document.createElement(heading ? 'th' : 'td');
    if (heading) cell.scope = 'row'; else cell.className = 'number';
    cell.textContent = text;
    if (detail) { const small = document.createElement('small'); small.textContent = detail; cell.append(small); }
    row.append(cell);
  }

  function addRow(item, label, isOriginal) {
    const row = document.createElement('tr');
    addCell(row, label, '', true);
    addCell(row, fileSize(item.bytes));
    addCell(row, isOriginal ? 'Reference size' : sizeChange(report.input.bytes, item.bytes));
    addCell(row, dimension(item));
    addCell(row, frameRate(item.fps), count(item.frames) + ' frames');
    addCell(row, isOriginal ? 'Reference' : similarity(item.ssim),
      !isOriginal && Number.isInteger(item.compared_frames) ? count(item.compared_frames) + ' frames compared' : '');
    addCell(row, isOriginal ? '—' : seconds(item.encode_seconds));
    addCell(row, isOriginal ? '—' : seconds(item.cpu_seconds));
    $('results-body').append(row);
  }

  function stopPlayback() { generation++; original.pause(); encoded.pause(); }

  function refreshReadiness() {
    const selectedURL = selected ? new URL(selected.url, window.location.href).href : '';
    const currentCandidate = encoded.currentSrc === selectedURL;
    sharedDuration = original.readyState >= 1 && encoded.readyState >= 1 && currentCandidate &&
      positive(original.duration) && positive(encoded.duration) ? Math.min(original.duration, encoded.duration) : 0;
    const ready = sharedDuration > 0 && !original.error && !encoded.error;
    for (const id of ['play', 'pause', 'restart', 'position']) $(id).disabled = !ready;
    $('position').max = String(sharedDuration || 1);
    if (ready) $('playback-status').textContent = 'Ready. Play both, or pause and move the position slider.';
    updatePosition();
  }

  function updatePosition() {
    const time = clampTime(original.currentTime, sharedDuration);
    $('position').value = String(time);
    $('position-label').textContent = `${time.toFixed(2)} / ${sharedDuration > 0 ? sharedDuration.toFixed(2) : '—'} s`;
  }

  function seekBoth(time) {
    const position = clampTime(time, sharedDuration);
    try { original.currentTime = position; encoded.currentTime = position; }
    catch { $('playback-status').textContent = 'The browser could not seek yet. Wait for both files to load.'; }
    updatePosition();
  }

  function selectCandidate(index) {
    stopPlayback();
    selected = choices[index];
    if (!selected) return;
    sharedDuration = 0;
    $('candidate-title').textContent = selected.label;
    $('candidate-description').textContent = `${dimension(selected.variant)} · ${frameRate(selected.variant.fps)} · ${fileSize(selected.variant.bytes)}`;
    $('playback-status').textContent = 'Loading the selected saved video…';
    encoded.src = selected.url;
    encoded.load();
    if (original.readyState > 0) original.currentTime = 0;
    for (const id of ['play', 'pause', 'restart', 'position']) $(id).disabled = true;
    updatePosition();
  }

  $('candidate').addEventListener('change', () => selectCandidate(Number($('candidate').value)));
  $('pause').addEventListener('click', () => { stopPlayback(); $('playback-status').textContent = 'Both videos paused. Frame alignment is approximate.'; });
  $('restart').addEventListener('click', () => { stopPlayback(); seekBoth(0); $('playback-status').textContent = 'Both videos returned to the beginning. Press Play both.'; });
  $('position').addEventListener('input', () => { stopPlayback(); seekBoth(Number($('position').value)); $('playback-status').textContent = 'Both videos paused at approximately the same time.'; });
  $('play').addEventListener('click', async () => {
    if (!sharedDuration) return;
    stopPlayback();
    const attempt = generation;
    const time = original.currentTime >= sharedDuration - 0.05 ? 0 : original.currentTime;
    seekBoth(time);
    try {
      await Promise.all([original.play(), encoded.play()]);
      if (attempt === generation) $('playback-status').textContent = 'Playing both. The browser may show nearby, rather than identical, source frames.';
    } catch {
      if (attempt === generation) { stopPlayback(); $('playback-status').textContent = 'Playback could not start for both files. Check that both videos have loaded, then try again.'; }
    }
  });
  for (const video of [original, encoded]) {
    video.addEventListener('loadedmetadata', refreshReadiness);
    video.addEventListener('durationchange', refreshReadiness);
    video.addEventListener('error', () => {
      stopPlayback(); refreshReadiness();
      $('playback-status').textContent = 'A saved video could not be loaded or played. The report remains available; this is not evidence that the other video is damaged.';
    });
    video.addEventListener('ended', () => { stopPlayback(); updatePosition(); $('playback-status').textContent = 'Comparison reached the end. Restart both to look again.'; });
  }
  original.addEventListener('timeupdate', () => {
    updatePosition();
    if (sharedDuration > 0 && original.currentTime >= sharedDuration && !original.paused) stopPlayback();
  });
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') stopPlayback(); });
  window.addEventListener('pagehide', stopPlayback);

  async function loadReport() {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch('/report.json', {cache: 'no-store', signal: controller.signal});
      if (!response.ok) throw new Error('report response');
      report = await response.json();
      if (!report || typeof report.input !== 'object' || !report.input || !Array.isArray(report.variants) || report.variants.length > 8) throw new Error('report shape');
      const provenance = reportProvenance(report);
      choices = report.variants.map(variant => {
        const media = mediaChoice(variant);
        return media ? {...media, variant} : null;
      }).filter(Boolean);
      if (choices.length === 0) throw new Error('no supported candidate');
      showProvenance(provenance);
      $('results-body').replaceChildren();
      addRow(report.input, provenance.kind === 'generated' ? 'Generated source' : 'Original recording', true);
      for (const choice of choices) addRow(choice.variant, choice.label, false);
      $('clip-description').textContent = `${provenance.kind === 'generated' ? 'Generated source' : 'Original'}: ${seconds(report.input.duration)} · ${dimension(report.input)} · ${frameRate(report.input.fps)}. Both alternatives target 20 fps; compare their frame counts and file sizes below.`;
      $('original-description').textContent = `${dimension(report.input)} · ${frameRate(report.input.fps)} · ${fileSize(report.input.bytes)}`;
      $('candidate').replaceChildren();
      choices.forEach((choice, index) => {
        const option = document.createElement('option'); option.value = String(index); option.textContent = choice.label; $('candidate').append(option);
      });
      $('candidate').disabled = false;
      $('created').textContent = typeof report.created_at === 'string' ? report.created_at : 'Unavailable';
      $('ffmpeg-version').textContent = typeof report.ffmpeg_version === 'string' ? report.ffmpeg_version : 'Unavailable';
      $('input-hash').textContent = typeof report.input.sha256 === 'string' && /^[a-fA-F0-9]{64}$/.test(report.input.sha256) ? report.input.sha256 : 'Unavailable';
      if (typeof report.comparison_note === 'string' && report.comparison_note.length <= 2000) {
        $('comparison-note').textContent = report.comparison_note; $('comparison-note').hidden = false;
      }
      // Only this verified playback copy has its timeline normalized to zero.
      // Size and checksum values above still describe the preserved input file.
      original.src = '/playback.mp4'; original.load(); selectCandidate(0);
      status(provenance.kind === 'generated'
        ? 'Generated scene report loaded. These saved test videos are ready to inspect once both files load.'
        : 'Report loaded. The saved videos are ready to inspect once both files load.');
    } catch {
      status('The local comparison report could not be loaded. Check that the comparison command finished and its viewer is still running, then reload this page.', true);
    } finally { clearTimeout(timeout); }
  }
  return loadReport();
}

if (typeof document !== 'undefined') initializePage(document);
