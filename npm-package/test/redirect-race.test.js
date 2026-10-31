#!/usr/bin/env node
// Standalone repro for beads#7780: downloadFile() in scripts/postinstall.js
// raced an async fs.unlink(dest) against a recursive re-open of the same
// `dest` path after following an HTTP redirect. Spins up a tiny local HTTP
// server that 302-redirects once then serves real bytes, runs downloadFile()
// against it many times, and asserts every run produced the exact bytes with
// no race-induced empty/missing file.
'use strict';

const http = require('http');
const fs = require('fs');
const os = require('os');
const path = require('path');

// Re-implement only the function under test, copied verbatim from
// scripts/postinstall.js so this harness exercises the real fixed/unfixed
// code path without depending on module structure. https is swapped for
// http so this can run against a local server with no TLS setup.
function downloadFile(url, dest, requestLib) {
  return new Promise((resolve, reject) => {
    const file = fs.createWriteStream(dest);

    const request = requestLib.get(url, (response) => {
      if (response.statusCode === 301 || response.statusCode === 302) {
        const redirectUrl = response.headers.location;
        response.destroy();
        request.destroy();
        file.close((closeErr) => {
          if (closeErr) {
            fs.unlink(dest, () => {});
            reject(closeErr);
            return;
          }
          downloadFile(redirectUrl, dest, requestLib).then(resolve).catch(reject);
        });
        return;
      }

      if (response.statusCode !== 200) {
        const error = new Error(`Failed to download: HTTP ${response.statusCode}`);
        response.destroy();
        request.destroy();
        file.close(() => {
          fs.unlink(dest, () => {});
          reject(error);
        });
        return;
      }

      response.on('error', (err) => {
        file.close(() => {
          fs.unlink(dest, () => {});
          reject(err);
        });
      });

      response.pipe(file);

      file.on('finish', () => {
        file.close((err) => {
          if (err) reject(err);
          else {
            response.destroy();
            resolve();
          }
        });
      });
    });

    request.on('error', (err) => {
      fs.unlink(dest, () => {});
      reject(err);
    });

    file.on('error', (err) => {
      fs.unlink(dest, () => {});
      reject(err);
    });
  });
}

const PAYLOAD = Buffer.from('fake-archive-bytes-'.repeat(2000)); // ~38KB, big enough to not finish in one syscall

function startServer() {
  return new Promise((resolveServer) => {
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
    server.listen(0, '127.0.0.1', () => resolveServer(server));
  });
}

async function runOnce(server, destDir, i) {
  const dest = path.join(destDir, `archive-${i}.bin`);
  const url = `http://127.0.0.1:${server.address().port}/redirect`;
  await downloadFile(url, dest, http);
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
  const server = await startServer();
  const destDir = fs.mkdtempSync(path.join(os.tmpdir(), 'bd-redirect-race-'));

  let completed = 0;
  let failures = [];

  async function worker(startIdx) {
    for (let i = startIdx; i < iterations; i += concurrency) {
      try {
        await runOnce(server, destDir, i);
        completed++;
      } catch (err) {
        failures.push(err.message);
      }
    }
  }

  await Promise.all(Array.from({ length: concurrency }, (_, w) => worker(w)));

  server.close();
  fs.rmSync(destDir, { recursive: true, force: true });

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
