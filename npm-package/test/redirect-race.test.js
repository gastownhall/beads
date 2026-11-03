#!/usr/bin/env node

/**
 * Regression tests for scripts/postinstall.js downloadFile().
 *
 * Runs without network access or a test framework:
 *   node npm-package/test/redirect-race.test.js
 */

const assert = require('assert');
const { execFileSync } = require('child_process');
const path = require('path');

const POSTINSTALL = path.join(__dirname, '..', 'scripts', 'postinstall.js');

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
  const env = Object.assign({}, process.env);
  delete env.CI;
  const out = execFileSync(process.execPath, ['-e', child, POSTINSTALL], {
    env,
    encoding: 'utf8',
    timeout: 10000,
  });
  const result = JSON.parse(out.trim().split('\n').pop());
  assert.deepStrictEqual(result.calls, [], 'require() started an installation');
  assert.strictEqual(result.downloadFile, 'function', 'downloadFile is not exported');
}

const TESTS = [
  ['require() exports downloadFile without installing', testRequireDoesNotInstall],
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

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
