const fs = require('fs');
const path = require('path');
const net = require('net');
const crypto = require('crypto');
const QRCode = require('qrcode');
const jsQR = require('jsqr');
const { PNG } = require('pngjs');

const validSource = id => typeof id === 'string' && /^[a-z][a-z0-9-]{0,31}$/.test(id);

function parseArgs(args, withWait) {
  const options = { root: process.cwd(), force: false, source: undefined, positional: [] };
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--root' || (arg === '--source' && !withWait)) {
      if (!args[i + 1] || args[i + 1].startsWith('--')) throw new Error('missing option');
      options[arg.slice(2)] = args[++i];
    } else if (arg === '--force') options.force = true;
    else if (arg.startsWith('--')) throw new Error('unknown option');
    else options.positional.push(arg);
  }
  options.root = path.resolve(options.root);
  if (withWait) {
    if (options.positional.length !== 2) throw new Error('source and wait required');
    [options.source, options.wait] = options.positional;
    if (!['80', '120', '300'].includes(options.wait)) throw new Error('invalid wait');
  } else if (options.positional.length) throw new Error('unexpected arguments');
  if (options.source !== undefined && !validSource(options.source)) throw new Error('invalid source');
  return options;
}

function readSettings(root, sourceID) {
  const settings = JSON.parse(fs.readFileSync(path.join(root, '.local/settings.json'), 'utf8'));
  const registered = Array.isArray(settings.sources) && settings.sources.length ? settings.sources : [{
    id: 'camera-01', publisher_user: settings.publisher_user,
    publisher_password: settings.publisher_password, publish_passphrase: settings.publish_passphrase,
  }];
  if (!net.isIP(settings.host) || registered.length > 4) {
    throw new Error('incomplete private settings');
  }
  const ids = new Set();
  const users = new Set();
  for (const source of registered) {
    if (!validSource(source.id) || ids.has(source.id) ||
        !/^[a-zA-Z0-9_-]{1,32}$/.test(source.publisher_user || '') ||
        source.publisher_user === 'any' || users.has(source.publisher_user) ||
        !/^[a-fA-F0-9]{32}$/.test(source.publisher_password || '') ||
        !/^[a-fA-F0-9]{32}$/.test(source.publish_passphrase || '')) {
      throw new Error('incomplete private source');
    }
    ids.add(source.id);
    users.add(source.publisher_user);
  }
  const sources = sourceID ? registered.filter(s => s.id === sourceID) : registered;
  if (!sources.length) throw new Error('source not registered');
  return { host: settings.host, sources };
}

function connectionFields(host, source, wait, overwrite) {
  const address = net.isIP(host) === 6 ? `[${host}]` : host;
  return [
    ['conn[][url]', `srt://${address}:18890`],
    ['conn[][name]', `Field Video ${source.id}`],
    ['conn[][overwrite]', overwrite ? 'on' : 'off'],
    ['conn[][active]', 'on'],
    ['conn[][mode]', 'v'],
    ['conn[][srtmode]', 'c'],
    ['conn[][srtlatency]', wait],
    ['conn[][srtpass]', source.publish_passphrase],
    ['conn[][srtpbkl]', '16'],
    ['conn[][srtstreamid]', `publish:${source.id}:${source.publisher_user}:${source.publisher_password}`],
  ];
}

async function prepareQR(host, source, wait, overwrite) {
  const fields = connectionFields(host, source, wait, overwrite);
  const uri = 'larix://set/v1?' + fields.map(([key, value]) => key + '=' + encodeURIComponent(value)).join('&');
  const buffer = await QRCode.toBuffer(uri, { type: 'png', errorCorrectionLevel: 'M', margin: 4, scale: 8 });
  const png = PNG.sync.read(buffer);
  const decoded = jsQR(new Uint8ClampedArray(png.data), png.width, png.height);
  if (!decoded || decoded.data !== uri) throw new Error('QR decode failed');
  const parsed = new URL(decoded.data);
  if (parsed.searchParams.size !== fields.length) throw new Error('unexpected import fields');
  for (const [key, value] of fields) {
    if (parsed.searchParams.get(key) !== value || key.startsWith('enc[')) throw new Error('QR fields differ');
  }
  return { buffer, uri };
}

function privateDirectory(directory) {
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  if (!fs.lstatSync(directory).isDirectory() || fs.lstatSync(directory).isSymbolicLink()) {
    throw new Error('output directory is not a real directory');
  }
  fs.chmodSync(directory, 0o700);
}

async function generate(options, withWait) {
  const { host, sources } = readSettings(options.root, options.source);
  const directory = path.join(options.root, '.local/connections');
  const prepared = [];
  for (const source of sources) {
    const wait = withWait ? options.wait : '300';
    const name = withWait
      ? `${source.id}-larix-${wait}ms${wait === '300' ? '-restore' : ''}-qr`
      : `${source.id}-larix-qr`;
    for (const ext of ['.png', '.txt']) {
      const filename = path.join(directory, name + ext);
      try {
        const stat = fs.lstatSync(filename);
        if (!options.force || !stat.isFile() || stat.isSymbolicLink()) throw new Error('output exists');
      } catch (err) {
        if (err.code !== 'ENOENT') throw err;
      }
    }
    prepared.push({ source, name, wait, ...await prepareQR(host, source, wait, withWait) });
  }
  privateDirectory(path.join(options.root, '.local'));
  privateDirectory(directory);
  for (const item of prepared) {
    for (const [ext, data] of [['.png', item.buffer], ['.txt', item.uri + '\n']]) {
      const target = path.join(directory, item.name + ext);
      if (!options.force) {
        fs.writeFileSync(target, data, { flag: 'wx', mode: 0o600 });
      } else {
        const temporary = target + '.tmp-' + crypto.randomBytes(8).toString('hex');
        try {
          fs.writeFileSync(temporary, data, { flag: 'wx', mode: 0o600 });
          fs.renameSync(temporary, target);
        } finally {
          try { fs.unlinkSync(temporary); } catch (err) { if (err.code !== 'ENOENT') throw err; }
        }
      }
    }
    console.log(`${item.source.id}: ${item.wait} ms QR decoded and verified; no encoder fields; private output files.`);
  }
}

module.exports = { parseArgs, readSettings, connectionFields, prepareQR, generate };
