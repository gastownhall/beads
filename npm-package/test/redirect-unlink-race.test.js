#!/usr/bin/env node

/**
 * Deterministic repro: downloadFile() following a 302 must leave the
 * downloaded archive in place.
 *
 * The redirect branch issues a fire-and-forget fs.unlink(dest) and then
 * re-downloads to the same dest. libuv gives no ordering between that unlink
 * and the new write, so this test forces the late interleaving: every
 * fs.unlink is held until downloadFile() resolves, then released.
 */

const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { EventEmitter } = require('events');
const { Readable } = require('stream');

const SCRIPT = path.join(__dirname, '..', 'scripts', 'postinstall.js');
const PAYLOAD = Buffer.from('real archive bytes');

const heldUnlinks = [];
const fakeFs = Object.assign({}, fs, {
  unlink(p, cb) {
    heldUnlinks.push(() => fs.unlink(p, cb));
  },
});

const fakeHttps = {
  get(url, onResponse) {
    const request = new EventEmitter();
    request.destroy = () => {};
    process.nextTick(() => {
      let response;
      if (url === 'https://example.test/redirect') {
        response = new Readable({ read() {} });
        response.statusCode = 302;
        response.headers = { location: 'https://example.test/asset' };
      } else {
        response = Readable.from([PAYLOAD]);
        response.statusCode = 200;
        response.headers = {};
      }
      onResponse(response);
    });
    return request;
  },
};

function loadDownloadFile() {
  const src = fs.readFileSync(SCRIPT, 'utf8').replace(/^#!.*\n/, '');
  const stubRequire = (name) => {
    if (name === 'https') return fakeHttps;
    if (name === 'fs') return fakeFs;
    if (name === '../package.json') return { version: '0.0.0-test' };
    return require(name);
  };
  const stubProcess = Object.create(process, { env: { value: { CI: '1' } } });
  const origLog = console.log;
  console.log = () => {};
  try {
    // The module stub keeps the source's module.exports seam evaluable in
    // this sandbox; the pre-seam source never references it.
    // eslint-disable-next-line no-new-func
    return new Function('require', 'process', 'module', '__dirname', `${src}\nreturn downloadFile;`)(
      stubRequire, stubProcess, { exports: {} }, path.dirname(SCRIPT));
  } finally {
    console.log = origLog;
  }
}

async function main() {
  const downloadFile = loadDownloadFile();
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'bd-redirect-race-'));
  const dest = path.join(dir, 'beads.tar.gz');

  const origLog = console.log;
  console.log = () => {};
  try {
    await downloadFile('https://example.test/redirect', dest);
  } finally {
    console.log = origLog;
  }

  // The download resolved; now let any held unlinks land, as libuv may.
  await Promise.all(heldUnlinks.map((run) => new Promise((resolve) => {
    run();
    setImmediate(resolve);
  })));
  await new Promise((resolve) => setTimeout(resolve, 50));

  assert.ok(fs.existsSync(dest),
    `downloaded archive ${dest} was deleted after downloadFile resolved ` +
    `(${heldUnlinks.length} unlink(s) issued during a successful redirect download)`);
  assert.deepStrictEqual(fs.readFileSync(dest), PAYLOAD);
  fs.rmSync(dir, { recursive: true, force: true });
  console.log('PASS: redirect download keeps the archive');
}

main().catch((err) => {
  console.error(`FAIL: ${err.message}`);
  process.exit(1);
});