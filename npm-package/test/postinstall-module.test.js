#!/usr/bin/env node

/**
 * Module-load contract for npm-package/scripts/postinstall.js.
 *
 * Requiring the postinstall module must be side-effect free — it exports
 * downloadFile and never starts an installation, even with CI unset — so
 * regression suites can exercise the real downloader. Direct execution
 * keeps its existing behavior (install unless CI is set).
 *
 * Each check runs in an isolated child process. Run:
 *   node npm-package/test/postinstall-module.test.js
 *   (or npm run test:postinstall from npm-package)
 */

'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');

const SCRIPT = path.join(__dirname, '..', 'scripts', 'postinstall.js');
const BIN_DIR = path.join(__dirname, '..', 'bin');
const CHILD_TIMEOUT_MS = 60000;

function runChild(args, env) {
  const result = spawnSync(process.execPath, args, {
    encoding: 'utf8',
    timeout: CHILD_TIMEOUT_MS,
    env,
  });
  if (result.error) {
    assert.fail(`child process ${args.join(' ')} failed: ${result.error.message}`);
  }
  return result;
}

function childEnvWithoutCI() {
  const env = Object.assign({}, process.env);
  delete env.CI;
  return env;
}

// Require the module with CI unset: nothing but the export may happen.
function requireMustBeSideEffectFree() {
  const binBefore = fs.readdirSync(BIN_DIR).sort().join(',');

  const probe = `
    const postinstall = require(process.argv[1]);
    if (typeof postinstall.downloadFile !== 'function') {
      console.error('downloadFile is not exported');
      process.exit(2);
    }
    console.log('MODULE_LOAD_OK');
    process.exit(0);
  `;
  const result = runChild(['-e', probe, SCRIPT], childEnvWithoutCI());

  assert.strictEqual(result.status, 0,
    `require() with CI unset must exit 0 (status=${result.status}, ` +
    `signal=${result.signal}, stderr=${JSON.stringify(result.stderr)})`);
  assert.strictEqual(result.stdout, 'MODULE_LOAD_OK\n',
    `requiring the module must not start an installation; stdout was ` +
    `${JSON.stringify(result.stdout)}`);
  assert.strictEqual(result.stderr, '',
    `requiring the module must not log errors; stderr was ` +
    `${JSON.stringify(result.stderr)}`);

  const binAfter = fs.readdirSync(BIN_DIR).sort().join(',');
  assert.strictEqual(binAfter, binBefore,
    'requiring the module must not mutate npm-package/bin');
}

// Direct execution with CI set keeps the existing skip behavior.
function directRunSkipsInCI() {
  const env = Object.assign({}, process.env, { CI: '1' });
  const result = runChild([SCRIPT], env);
  assert.strictEqual(result.status, 0, 'CI=1 direct run must exit 0');
  assert.ok(result.stdout.includes('Skipping binary download in CI environment'),
    `CI=1 direct run must print the skip message; stdout was ` +
    `${JSON.stringify(result.stdout)}`);
}

function main() {
  requireMustBeSideEffectFree();
  console.log('ok: require() with CI unset exports downloadFile and starts no installation');
  directRunSkipsInCI();
  console.log('ok: direct execution with CI=1 still skips the download');
  console.log('PASS: postinstall module-load contract holds');
}

try {
  main();
} catch (err) {
  console.error(`FAIL: ${err.message}`);
  process.exit(1);
}