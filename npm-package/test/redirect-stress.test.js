#!/usr/bin/env node

/**
 * Supplementary stress harness for the postinstall redirect download (#7791).
 *
 * Runs the real downloadFile() from scripts/postinstall.js many times under
 * concurrency against a local HTTP server that 302-redirects once and then
 * serves real bytes, and asserts every run left exactly those bytes. The old
 * redirect-path fs.unlink raced the recursive re-open of the same path, so a
 * late unlink could delete the finished archive. That race is
 * timing-dependent: a pass here is supporting evidence only. The decisive
 * regression is the deterministic test/redirect-race.test.js.
 *
 * Not part of `npm run test:all`. Run:
 *   node npm-package/test/redirect-stress.test.js [iterations] [concurrency]
 */

'use strict';

const fs = require('fs');
const http = require('http');
const https = require('https');
const os = require('os');
const path = require('path');

const POSTINSTALL = path.join(__dirname, '..', 'scripts', 'postinstall.js');
const PAYLOAD = Buffer.from('fake-archive-bytes-'.repeat(2000)); // ~38KB, big enough to not finish in one syscall

function startServer() {
  return new Promise((resolveServer, rejectServer) => {
    const server = http.createServer((req, res) => {
      if (req.url === '/redirect') {
        res.writeHead(302, { Location: `http://127.0.0.1:${server.address().port}/real` });
        res.end();
        return;
      }
      if (req.url === '/real') {
        res.writeHead(200, { 'Content-Length': PAYLOAD.length });
        res.end(PAYLOAD);
        return;
      }
      res.writeHead(404);
      res.end();
    });
    server.once('error', rejectServer);
    server.listen(0, '127.0.0.1', () => resolveServer(server));
  });
}

async function runOnce(downloadFile, server, destDir, i) {
  const dest = path.join(destDir, `archive-${i}.bin`);
  const url = `http://127.0.0.1:${server.address().port}/redirect`;
  await downloadFile(url, dest);
  const got = fs.readFileSync(dest);
  fs.unlinkSync(dest);
  if (!got.equals(PAYLOAD)) {
    throw new Error(
      `run ${i}: dest has ${got.length} bytes, expected ${PAYLOAD.length} (race reproduced: ` +
      `file was truncated/emptied by a stray unlink racing the re-open)`
    );
  }
}

async function main() {
  const iterations = parseInt(process.argv[2] || '50', 10);
  const concurrency = parseInt(process.argv[3] || '8', 10);
  const { downloadFile } = require(POSTINSTALL);

  let completed = 0;
  const failures = [];
  const origHttpsGet = https.get;
  const origLog = console.log;
  let server = null;
  let destDir = null;
  try {
    // postinstall.js downloads with https.get; serve it from the plain-http
    // local server and silence its per-download progress lines.
    https.get = http.get;
    console.log = () => {};
    server = await startServer();
    destDir = fs.mkdtempSync(path.join(os.tmpdir(), 'bd-redirect-stress-'));

    async function worker(startIdx) {
      for (let i = startIdx; i < iterations; i += concurrency) {
        try {
          await runOnce(downloadFile, server, destDir, i);
          completed++;
        } catch (err) {
          // ENOENT from readFileSync is the race too: the archive was deleted.
          failures.push(err.message);
        }
      }
    }

    await Promise.all(Array.from({ length: concurrency }, (_, w) => worker(w)));
  } finally {
    https.get = origHttpsGet;
    console.log = origLog;
    if (server) await new Promise((resolve) => server.close(() => resolve()));
    if (destDir) fs.rmSync(destDir, { recursive: true, force: true });
  }

  console.log(`${completed}/${iterations} runs produced correct bytes (concurrency=${concurrency}).`);
  if (failures.length > 0) {
    console.error(`${failures.length} FAILURES:`);
    failures.slice(0, 5).forEach((m) => console.error(`  - ${m}`));
    process.exit(1);
  }
  console.log('PASS: no race reproduced.');
}

main().catch((err) => {
  console.error('Harness error:', err);
  process.exit(1);
});
