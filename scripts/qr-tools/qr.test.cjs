const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const { PNG } = require('pngjs');
const jsQR = require('jsqr');
const { readSettings } = require('./common.cjs');

test('credential validation accepts source IDs and legacy usernames under Go rules', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'videolab-qr-validation-'));
  try {
    fs.mkdirSync(path.join(root, '.local'));
    const filename = path.join(root, '.local/settings.json');
    const fixture = user => ({id:'camera-01', publisher_user:user, publisher_password:'A'.repeat(32), publish_passphrase:'B'.repeat(32)});
    for (const user of ['camera-01', 'camera-02', 'pub_0123', '_Legacy-User']) {
      fs.writeFileSync(filename, JSON.stringify({host:'192.0.2.10', sources:[fixture(user)]}));
      assert.equal(readSettings(root).sources[0].publisher_user, user);
    }
    for (const user of ['any', 'a'.repeat(33), 'user:password', '']) {
      fs.writeFileSync(filename, JSON.stringify({host:'192.0.2.10', sources:[fixture(user)]}));
      assert.throws(() => readSettings(root));
    }
    fs.writeFileSync(filename, JSON.stringify({host:'192.0.2.10', ...fixture('legacy_user')}));
    assert.equal(readSettings(root).sources[0].id, 'camera-01');
  } finally {
    fs.rmSync(root, {recursive:true, force:true});
  }
});

test('portable CLI generates verified private imports without changing settings', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'videolab-qr-test-'));
  try {
    fs.mkdirSync(path.join(root, '.local'));
    // Public test fixtures, unrelated to any real source credentials.
    const source = {id:'camera-01', publisher_user:'camera-01', publisher_password:'1'.repeat(32), publish_passphrase:'2'.repeat(32)};
    const settings = JSON.stringify({host:'192.0.2.10', sources:[source]});
    const settingsPath = path.join(root, '.local/settings.json');
    fs.writeFileSync(settingsPath, settings);
    const invoke = (file, ...args) => spawnSync(process.execPath, [path.join(__dirname, file), ...args, '--root', root], {encoding:'utf8'});
    const original = invoke('generate.cjs');
    assert.equal(original.status, 0, original.stderr);
    const adjusted = invoke('generate-wait.cjs', 'camera-01', '80');
    assert.equal(adjusted.status, 0, adjusted.stderr);
    for (const [name, wait, overwrite] of [['camera-01-larix-qr','300','off'], ['camera-01-larix-80ms-qr','80','on']]) {
      const filename = path.join(root, '.local/connections', name);
      const png = PNG.sync.read(fs.readFileSync(filename + '.png'));
      const decoded = jsQR(new Uint8ClampedArray(png.data), png.width, png.height);
      assert.ok(decoded);
      assert.equal(decoded.data + '\n', fs.readFileSync(filename + '.txt', 'utf8'));
      const fields = new URL(decoded.data).searchParams;
      assert.equal(fields.size, 10);
      assert.equal(fields.get('conn[][srtlatency]'), wait);
      assert.equal(fields.get('conn[][overwrite]'), overwrite);
      assert.equal(fields.get('conn[][srtpass]'), source.publish_passphrase);
      assert.equal(fields.get('conn[][srtstreamid]'), `publish:camera-01:camera-01:${source.publisher_password}`);
      assert.ok([...fields.keys()].every(k => !k.startsWith('enc[')));
      for (const ext of ['.png','.txt']) assert.equal(fs.statSync(filename+ext).mode & 0o777, 0o600);
    }
    assert.equal(fs.readFileSync(settingsPath, 'utf8'), settings);
    assert.equal(invoke('generate.cjs').status, 1, 'existing imports require explicit replacement');
    assert.equal(invoke('generate-wait.cjs', '../escape', '80').status, 1);
    assert.equal(invoke('generate-wait.cjs', 'camera-01', '60').status, 1);
    const combined = original.stdout + original.stderr + adjusted.stdout + adjusted.stderr;
    assert.ok(!combined.includes(source.publish_passphrase));
    assert.ok(!combined.includes(source.publisher_password));
    assert.ok(!combined.includes('larix://'));
  } finally {
    fs.rmSync(root, {recursive:true, force:true});
  }
});
