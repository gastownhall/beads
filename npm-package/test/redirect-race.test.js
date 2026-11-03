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
const path = require('path');

const POSTINSTALL = path.join(__dirname, '..', 'scripts', 'postinstall.js');
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

const TESTS = [
  ['require() exports downloadFile without installing', testRequireDoesNotInstall],
  ['direct run with CI=1 skips the download', testDirectRunSkipsInCI],
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

// A test that never settles must fail the suite, not
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
