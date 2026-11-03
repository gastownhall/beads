#!/usr/bin/env node

/**
 * Deterministic regression suite for the postinstall redirect unlink race.
 *
 * downloadFile() follows GitHub release redirects (301/302) by recursing
 * with the same destination path. The redirect branch used to fire an
 * unawaited fs.unlink(dest) immediately before recursing; both that unlink
 * and the recursive download's file operations run on libuv's thread pool
 * with no ordering guarantee, so a late unlink could delete the finished
 * archive right after the download resolved (tar: No such file or
 * directory).
 *
 * Instead of racing on threadpool timing, this suite selects the failing
 * schedule deterministically: while a download is in flight every fs.unlink
 * call is recorded rather than executed; once the download promise settles
 * (resolved or rejected) the recorded calls are executed with the original
 * unlink and awaited to completion before the destination is inspected. On
 * the unfixed code the held redirect-path unlink removes the archive after
 * resolution; on the fixed code no unlink is ever scheduled on the
 * redirect-success path.
 *
 * The download runs against a local HTTP server (127.0.0.1, no external
 * network); the module's https.get is temporarily mapped to http.get.
 *
 * Run: node npm-package/test/redirect-race.test.js
 *      (or npm run test:postinstall from npm-package)
 */

'use strict';

const assert = require('assert');
const fs = require('fs');
const http = require('http');
const https = require('https');
const os = require('os');
const path = require('path');

const SCRIPT = path.join(__dirname, '..', 'scripts', 'postinstall.js');
const SUITE_TIMEOUT_MS = 30000;

const PAYLOAD = Buffer.concat([
  Buffer.from('real archive bytes\n'),
  Buffer.alloc(4096, 0x61),
]);
// Longer than any download payload, to prove truncation of stale content.
const STALE = Buffer.alloc(PAYLOAD.length + 2048, 0x78);

// --- shared-module patches (restored in main's finally) ---------------------

const realUnlink = fs.unlink;
const realHttpsGet = https.get;
let holding = false;
const heldUnlinks = [];

// While holding is on, record unlink calls instead of executing them.
fs.unlink = function heldUnlink(target, callback) {
  if (!holding) {
    return realUnlink.call(fs, target, callback);
  }
  heldUnlinks.push({ target, callback });
};

// Execute every held unlink with the original function and await its
// callback. Terminal-error paths may schedule duplicate cleanup calls, so
// ENOENT is expected and allowed; any other error fails the suite.
function flushHeldUnlinks() {
  const calls = heldUnlinks.splice(0);
  return Promise.all(calls.map((call) => new Promise((resolve, reject) => {
    realUnlink.call(fs, call.target, (err) => {
      if (err && err.code !== 'ENOENT') {
        reject(err);
      } else {
        resolve();
      }
    });
  })));
}

// Run a download with unlinks held. Once its promise settles — resolution
// or rejection — the held unlinks are executed to completion, mimicking a
// libuv thread pool that lands the unlink after the download finished.
async function withHeldUnlinks(download) {
  assert.strictEqual(heldUnlinks.length, 0, 'unlinks left over from a previous case');
  holding = true;
  try {
    try {
      const result = await download();
      await flushHeldUnlinks();
      return result;
    } catch (err) {
      await flushHeldUnlinks();
      throw err;
    }
  } finally {
    holding = false;
  }
}

// Map the module's https.get to the local test server's plain http.
https.get = function localGet(url, onResponse) {
  return http.get(url, onResponse);
};

// --- local server -----------------------------------------------------------

let server = null;
let base = null;

function startServer() {
  return new Promise((resolve) => {
    server = http.createServer((req, res) => {
      if (req.url === '/asset') {
        res.writeHead(200, { 'content-length': String(PAYLOAD.length) });
        res.end(PAYLOAD);
      } else if (req.url === '/redirect') {
        res.writeHead(302, { location: `${base}/asset` });
        res.end();
      } else if (req.url === '/redirect-301') {
        res.writeHead(301, { location: `${base}/redirect` });
        res.end();
      } else if (req.url === '/redirect-missing') {
        res.writeHead(302, { location: `${base}/missing` });
        res.end();
      } else if (req.url === '/missing') {
        res.writeHead(404, { 'content-length': '9' });
        res.end('not found');
      } else {
        res.writeHead(500, { 'content-length': '0' });
        res.end();
      }
    });
    server.listen(0, '127.0.0.1', () => {
      base = `http://127.0.0.1:${server.address().port}`;
      resolve();
    });
  });
}

function closeServer() {
  return new Promise((resolve) => server.close(resolve));
}

// --- cases (serial: they share patched fs/https state) -----------------------

function assertDownloadedBytes(dest, label) {
  assert.ok(fs.existsSync(dest),
    `${label}: destination was deleted after downloadFile resolved`);
  const got = fs.readFileSync(dest);
  assert.ok(PAYLOAD.equals(got),
    `${label}: expected ${PAYLOAD.length} payload bytes, got ${got.length}`);
}

async function caseDirect200(downloadFile, dir) {
  const dest = path.join(dir, 'direct.tar.gz');
  await withHeldUnlinks(() => downloadFile(`${base}/asset`, dest));
  assertDownloadedBytes(dest, 'direct 200');
}

async function caseRedirect302(downloadFile, dir) {
  const dest = path.join(dir, 'redirect.tar.gz');
  await withHeldUnlinks(() => downloadFile(`${base}/redirect`, dest));
  assertDownloadedBytes(dest, '302 redirect');
}

async function caseRedirectChain(downloadFile, dir) {
  const dest = path.join(dir, 'chain.tar.gz');
  await withHeldUnlinks(() => downloadFile(`${base}/redirect-301`, dest));
  assertDownloadedBytes(dest, '301 -> 302 -> 200 chain');
}

async function caseStaleContentIsTruncated(downloadFile, dir) {
  const dest = path.join(dir, 'stale.tar.gz');
  fs.writeFileSync(dest, STALE);
  await withHeldUnlinks(() => downloadFile(`${base}/redirect`, dest));
  assertDownloadedBytes(dest, 'redirect over longer stale content');
}

async function caseRedirectTo404(downloadFile, dir) {
  const dest = path.join(dir, 'missing.tar.gz');
  let rejected = null;
  try {
    await withHeldUnlinks(() => downloadFile(`${base}/redirect-missing`, dest));
  } catch (err) {
    rejected = err;
  }
  assert.ok(rejected, 'redirect to 404 must reject');
  assert.match(rejected.message, /HTTP 404/,
    'redirect to 404 must reject with the HTTP error');
  assert.ok(!fs.existsSync(dest),
    'destination must be absent after queued cleanup completes');
}

// --- main -------------------------------------------------------------------

async function main() {
  // Load the real module without triggering the install dispatch: the
  // require.main guard normally prevents it, but set CI for this process
  // too so loading pre-guard (unfixed) code is equally side-effect free.
  // downloadFile itself never consults CI.
  const hadCI = Object.prototype.hasOwnProperty.call(process.env, 'CI');
  const savedCI = process.env.CI;
  process.env.CI = '1';

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'bd-redirect-race-'));
  let downloadFile;
  try {
    ({ downloadFile } = require(SCRIPT));
    assert.strictEqual(typeof downloadFile, 'function',
      'postinstall.js must export downloadFile');
    await startServer();

    await caseDirect200(downloadFile, dir);
    console.log('ok: direct 200 download preserves exact bytes');
    await caseRedirect302(downloadFile, dir);
    console.log('ok: 302 redirect download keeps the archive once held unlinks land');
    await caseRedirectChain(downloadFile, dir);
    console.log('ok: 301 -> 302 -> 200 chain preserves exact bytes');
    await caseStaleContentIsTruncated(downloadFile, dir);
    console.log('ok: redirected download truncates longer stale content');
    await caseRedirectTo404(downloadFile, dir);
    console.log('ok: redirect to 404 rejects and cleans up the destination');
  } finally {
    if (server) {
      await closeServer();
    }
    fs.rmSync(dir, { recursive: true, force: true });
    fs.unlink = realUnlink;
    https.get = realHttpsGet;
    if (hadCI) {
      process.env.CI = savedCI;
    } else {
      delete process.env.CI;
    }
  }
  console.log('PASS: redirect download regression suite');
}

const suiteTimeout = setTimeout(() => {
  console.error('FAIL: redirect-race suite timed out');
  process.exit(1);
}, SUITE_TIMEOUT_MS);
suiteTimeout.unref();

main().then(() => {
  clearTimeout(suiteTimeout);
}, (err) => {
  clearTimeout(suiteTimeout);
  console.error(`FAIL: ${err && err.message ? err.message : err}`);
  process.exit(1);
});