#!/usr/bin/env node

/**
 * Regression tests for scripts/postinstall.js: downloadFile() and the
 * module-load contract that lets these tests require the real function.
 *
 * Runs serially in one process, without network access or a test framework:
 *   node npm-package/test/redirect-race.test.js
 *   (or npm run test:postinstall from npm-package)
 */

'use strict';

const assert = require('assert');
const { execFileSync, spawnSync } = require('child_process');
const fs = require('fs');
const http = require('http');
const https = require('https');
const os = require('os');
const path = require('path');

const POSTINSTALL = path.join(__dirname, '..', 'scripts', 'postinstall.js');
const PAYLOAD = Buffer.from('known archive bytes\n'.repeat(64));
const CHILD_TIMEOUT_MS = 10000;
const SUITE_TIMEOUT_MS = 30000;

function envWithoutCI() {
  const env = Object.assign({}, process.env);
  delete env.CI;
  return env;
}

// Requiring postinstall.js with CI unset must export downloadFile without
// starting an installation. The check runs in a child process whose network,
// filesystem-mutation and exit entry points are traps, so a regression cannot
// download a release or write into npm-package/bin.
function testRequireDoesNotInstall() {
  const child = `
    const fs = require('fs');
    const http = require('http');
    const https = require('https');
    const calls = [];
    const trap = (name) => function () {
      calls.push(name);
      throw new Error('trapped ' + name);
    };
    for (const m of ['get', 'request']) {
      https[m] = trap('https.' + m);
      http[m] = trap('http.' + m);
    }
    for (const m of ['createWriteStream', 'mkdirSync', 'writeFileSync', 'unlink',
      'unlinkSync', 'chmodSync', 'renameSync', 'rmSync']) {
      fs[m] = trap('fs.' + m);
    }
    require('child_process').execSync = trap('child_process.execSync');
    process.exit = (code) => { calls.push('process.exit(' + code + ')'); };
    const console_log = console.log;
    console.log = () => {};
    console.error = () => {};
    const mod = require(process.argv[1]);
    setTimeout(() => {
      console_log(JSON.stringify({ downloadFile: typeof mod.downloadFile, calls }));
    }, 50);
  `;
  const out = execFileSync(process.execPath, ['-e', child, POSTINSTALL], {
    env: envWithoutCI(),
    encoding: 'utf8',
    timeout: CHILD_TIMEOUT_MS,
  });
  const result = JSON.parse(out.trim().split('\n').pop());
  assert.deepStrictEqual(result.calls, [], 'require() started an installation');
  assert.strictEqual(result.downloadFile, 'function', 'downloadFile is not exported');
}

// Direct execution (npm postinstall) with CI set keeps the existing skip.
function testDirectRunSkipsInCI() {
  const result = spawnSync(process.execPath, [POSTINSTALL], {
    env: Object.assign(envWithoutCI(), { CI: '1' }),
    encoding: 'utf8',
    timeout: CHILD_TIMEOUT_MS,
  });
  if (result.error) throw result.error;
  assert.strictEqual(result.status, 0, `CI=1 direct run exited ${result.status}: ${result.stderr}`);
  assert.ok(result.stdout.includes('Skipping binary download in CI environment'),
    `CI=1 direct run did not print the skip message: ${JSON.stringify(result.stdout)}`);
}

// Local server standing in for GitHub releases: /archive serves PAYLOAD and
// the other routes redirect to it, or to a 404, the way release downloads do.
function startServer() {
  const routes = {
    '/archive': { status: 200 },
    '/redirect-302': { status: 302, location: '/archive' },
    '/redirect-301': { status: 301, location: '/redirect-302' },
    '/redirect-404': { status: 302, location: '/missing' },
  };
  const server = http.createServer((req, res) => {
    const route = routes[req.url];
    if (!route) {
      res.writeHead(404);
      res.end('not found');
    } else if (route.location) {
      res.writeHead(route.status, { Location: base + route.location });
      res.end();
    } else {
      res.writeHead(200, { 'Content-Length': PAYLOAD.length });
      res.end(PAYLOAD);
    }
  });
  let base;
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      base = `http://127.0.0.1:${server.address().port}`;
      resolve({ server, base });
    });
  });
}

// Runs downloadFile(url, dest) against the local server while holding every
// fs.unlink it schedules until the download settles, then runs the held
// unlinks to completion. libuv gives no ordering between such an unlink and
// the recursive download's open of the same path, so this selects the late
// schedule deterministically. Returns { error } (null on resolve).
async function downloadHoldingUnlinks(url, dest) {
  const { downloadFile } = require(POSTINSTALL);
  const origUnlink = fs.unlink;
  const origHttpsGet = https.get;
  const origLog = console.log;
  const held = [];
  fs.unlink = (p, cb) => {
    held.push([p, cb]);
  };
  https.get = http.get;
  console.log = () => {};
  let error = null;
  try {
    try {
      await downloadFile(url, dest);
    } catch (err) {
      error = err;
    }
  } finally {
    fs.unlink = origUnlink;
    https.get = origHttpsGet;
    console.log = origLog;
  }
  for (const [p, cb] of held) {
    await new Promise((resolve, reject) => {
      origUnlink(p, (err) => {
        cb(err);
        // A duplicate cleanup of an already removed file is fine.
        if (err && err.code !== 'ENOENT') reject(err);
        else resolve();
      });
    });
  }
  return { error };
}

async function withServerAndDir(fn) {
  const { server, base } = await startServer();
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'bd-postinstall-test-'));
  try {
    await fn(base, path.join(dir, 'beads.tar.gz'));
  } finally {
    await new Promise((resolve) => server.close(() => resolve()));
    for (const name of fs.readdirSync(dir)) {
      fs.unlinkSync(path.join(dir, name));
    }
    fs.rmdirSync(dir);
  }
}

function assertArchive(dest) {
  assert.ok(fs.existsSync(dest), `archive ${dest} is missing after the download resolved`);
  assert.ok(fs.readFileSync(dest).equals(PAYLOAD), 'archive bytes differ from the served bytes');
}

function expectArchive(route, seed) {
  return () => withServerAndDir(async (base, dest) => {
    if (seed) fs.writeFileSync(dest, seed);
    const { error } = await downloadHoldingUnlinks(base + route, dest);
    if (error) throw error;
    assertArchive(dest);
  });
}

function testRedirectTo404() {
  return withServerAndDir(async (base, dest) => {
    const { error } = await downloadHoldingUnlinks(`${base}/redirect-404`, dest);
    assert.ok(error, 'download of a redirect to 404 resolved');
    assert.strictEqual(error.message, 'Failed to download: HTTP 404');
    assert.ok(!fs.existsSync(dest), 'failed download left a file behind');
  });
}

// The npm package CI gate (scripts/ci/package-npm.sh) runs `npm run test:all`.
// This suite must be one of its &&-chained steps so a failure here fails it.
function testWiredIntoTestAll() {
  const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'));
  const self = `node test/${path.basename(__filename)}`;
  assert.strictEqual(pkg.scripts['test:postinstall'], self, 'test:postinstall does not run this suite');
  const steps = pkg.scripts['test:all'].split('&&').map((step) => step.trim());
  assert.deepStrictEqual(steps, ['npm test', 'npm run test:postinstall', 'npm run test:integration'],
    'test:all does not chain test:postinstall with &&');
}

const TESTS = [
  ['302 redirect keeps the archive after held unlinks run', expectArchive('/redirect-302')],
  ['301 -> 302 -> 200 chain keeps the archive', expectArchive('/redirect-301')],
  ['direct 200 keeps the archive', expectArchive('/archive')],
  ['redirect truncates longer stale content', expectArchive('/redirect-302', Buffer.alloc(PAYLOAD.length * 3, 'x'))],
  ['redirect to 404 rejects and removes the file', testRedirectTo404],
  ['require() exports downloadFile without installing', testRequireDoesNotInstall],
  ['direct run with CI=1 skips the download', testDirectRunSkipsInCI],
  ['npm run test:all runs this suite', testWiredIntoTestAll],
];

async function main() {
  let failed = 0;
  for (const [name, fn] of TESTS) {
    try {
      await fn();
      console.log(`✓ ${name}`);
    } catch (err) {
      failed++;
      console.error(`✗ ${name}: ${err.message}`);
    }
  }
  if (failed > 0) {
    console.error(`\n${failed} of ${TESTS.length} test(s) failed`);
    process.exit(1);
  }
  console.log(`\nAll ${TESTS.length} test(s) passed`);
}

// A download or server shutdown that never settles must fail the suite, not
// hang the npm gate.
const suiteTimeout = setTimeout(() => {
  console.error(`✗ suite timed out after ${SUITE_TIMEOUT_MS} ms`);
  process.exit(1);
}, SUITE_TIMEOUT_MS);
suiteTimeout.unref();

main().then(() => clearTimeout(suiteTimeout), (err) => {
  console.error(err);
  process.exit(1);
});
