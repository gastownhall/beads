#!/usr/bin/env node

const { spawn } = require('child_process');
const path = require('path');
const os = require('os');
const fs = require('fs');

// Determine the platform-specific binary name
function getBinaryPath() {
  const platform = os.platform();
  const arch = os.arch();

  let binaryName = 'bd';
  if (platform === 'win32') {
    binaryName = 'bd.exe';
  }

  // Binary is stored in the package's bin directory
  const binaryPath = path.join(__dirname, binaryName);

  if (!fs.existsSync(binaryPath)) {
    console.error(`Error: bd binary not found at ${binaryPath}`);
    console.error('This may indicate that the postinstall script failed to download the binary.');
    console.error(`Platform: ${platform}, Architecture: ${arch}`);
    process.exit(1);
  }

  return binaryPath;
}

// Execute the native binary with all arguments passed through
function main() {
  const binaryPath = getBinaryPath();

  // Spawn the native bd binary with all command-line arguments
  const child = spawn(binaryPath, process.argv.slice(2), {
    stdio: 'inherit',
    env: process.env
  });

  child.on('error', (err) => {
    console.error(`Error executing bd binary: ${err.message}`);
    process.exit(1);
  });

  child.on('exit', (code, signal) => {
    if (signal) {
      process.exit(1);
    }
    process.exit(code || 0);
  });
}

main();
