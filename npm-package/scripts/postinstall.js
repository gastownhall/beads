#!/usr/bin/env node

const https = require('https');
const fs = require('fs');
const path = require('path');
const os = require('os');
const { execSync } = require('child_process');

// Get package version to determine which release to download
const packageJson = require('../package.json');
const VERSION = packageJson.version;

// Determine platform and architecture
function getPlatformInfo() {
  const platform = os.platform();
  const arch = os.arch();

  let platformName;
  let archName;
  let binaryName = 'bd';

  // Map Node.js platform names to GitHub release names
  switch (platform) {
    case 'darwin':
      platformName = 'darwin';
      break;
    case 'linux':
      platformName = 'linux';
      break;
    case 'win32':
      platformName = 'windows';
      binaryName = 'bd.exe';
      break;
    default:
      throw new Error(`Unsupported platform: ${platform}`);
  }

  // Map Node.js arch names to GitHub release names
  switch (arch) {
    case 'x64':
      archName = 'amd64';
      break;
    case 'arm64':
      archName = 'arm64';
      break;
    default:
      throw new Error(`Unsupported architecture: ${arch}`);
  }

  return { platformName, archName, binaryName };
}

// Download file from URL
function downloadFile(url, dest) {
  return new Promise((resolve, reject) => {
    console.log(`Downloading from: ${url}`);
    const file = fs.createWriteStream(dest);

    const request = https.get(url, (response) => {
      // Handle redirects
      if (response.statusCode === 301 || response.statusCode === 302) {
        const redirectUrl = response.headers.location;
        console.log(`Following redirect to: ${redirectUrl}`);
        downloadFile(redirectUrl, dest).then(resolve).catch(reject);
        return;
      }

      if (response.statusCode !== 200) {
        reject(new Error(`Failed to download: HTTP ${response.statusCode}`));
        return;
      }

      response.pipe(file);

      file.on('finish', () => {
        // Wait for file.close() to complete before resolving
        // This is critical on Windows where the file may still be locked
        file.close((err) => {
          if (err) reject(err);
          else resolve();
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

// Extract tar.gz file
function extractTarGz(tarGzPath, destDir, binaryName) {
  console.log(`Extracting ${tarGzPath}...`);

  try {
    // Use tar command to extract
    execSync(`tar -xzf "${tarGzPath}" -C "${destDir}"`, { stdio: 'inherit' });

    // The binary should now be in destDir
    const extractedBinary = path.join(destDir, binaryName);

    if (!fs.existsSync(extractedBinary)) {
      throw new Error(`Binary not found after extraction: ${extractedBinary}`);
    }

    // Make executable on Unix-like systems
    if (os.platform() !== 'win32') {
      fs.chmodSync(extractedBinary, 0o755);
    }

    console.log(`Binary extracted to: ${extractedBinary}`);
  } catch (err) {
    throw new Error(`Failed to extract archive: ${err.message}`);
  }
}

// Extract zip file (for Windows)
function extractZip(zipPath, destDir, binaryName) {
  console.log(`Extracting ${zipPath}...`);

  try {
    // Use unzip command or powershell on Windows
    if (os.platform() === 'win32') {
      execSync(`powershell -command "Expand-Archive -Path '${zipPath}' -DestinationPath '${destDir}' -Force"`, { stdio: 'inherit' });
    } else {
      execSync(`unzip -o "${zipPath}" -d "${destDir}"`, { stdio: 'inherit' });
    }

    // The binary should now be in destDir
    const extractedBinary = path.join(destDir, binaryName);

    if (!fs.existsSync(extractedBinary)) {
      throw new Error(`Binary not found after extraction: ${extractedBinary}`);
    }

    console.log(`Binary extracted to: ${extractedBinary}`);
  } catch (err) {
    throw new Error(`Failed to extract archive: ${err.message}`);
  }
}

// Main installation function
async function install() {
  try {
    const { platformName, archName, binaryName } = getPlatformInfo();

    console.log(`Installing bd v${VERSION} for ${platformName}-${archName}...`);

    // Construct download URL
    // Format: https://github.com/steveyegge/beads/releases/download/v0.21.5/beads_0.21.5_darwin_amd64.tar.gz
    const releaseVersion = VERSION;
    const archiveExt = platformName === 'windows' ? 'zip' : 'tar.gz';
    const archiveName = `beads_${releaseVersion}_${platformName}_${archName}.${archiveExt}`;
    const downloadUrl = `https://github.com/steveyegge/beads/releases/download/v${releaseVersion}/${archiveName}`;

    // Determine destination paths
    const binDir = path.join(__dirname, '..', 'bin');
    const archivePath = path.join(binDir, archiveName);
    const binaryPath = path.join(binDir, binaryName);

    // Ensure bin directory exists
    if (!fs.existsSync(binDir)) {
      fs.mkdirSync(binDir, { recursive: true });
    }

    // Download the archive
    console.log(`Downloading bd binary...`);
    await downloadFile(downloadUrl, archivePath);

    // Extract the archive based on platform
    if (platformName === 'windows') {
      extractZip(archivePath, binDir, binaryName);
    } else {
      extractTarGz(archivePath, binDir, binaryName);
    }

    // Clean up archive
    fs.unlinkSync(archivePath);

    // Verify the binary works
    try {
      const output = execSync(`"${binaryPath}" version`, { encoding: 'utf8' });
      console.log(`✓ bd installed successfully: ${output.trim()}`);
    } catch (err) {
      console.warn('Warning: Could not verify binary version');
    }

  } catch (err) {
    console.error(`Error installing bd: ${err.message}`);
    console.error('');
    console.error('Installation failed. You can try:');
    console.error('1. Installing manually from: https://github.com/steveyegge/beads/releases');
    console.error('2. Using the install script: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash');
    console.error('3. Opening an issue: https://github.com/steveyegge/beads/issues');
    process.exit(1);
  }
}

// Run installation if not in CI environment
if (!process.env.CI) {
  install();
} else {
  console.log('Skipping binary download in CI environment');
}
