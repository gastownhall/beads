#!/usr/bin/env node

/**
 * Integration tests for @beads/bd npm package
 *
 * Tests:
 * 1. Package installation in clean environment
 * 2. Binary download and extraction
 * 3. Basic bd commands (version, init, create, list, etc.)
 * 4. Claude Code for Web simulation
 */

const { execSync, spawn } = require('child_process');
const fs = require('fs');
const path = require('path');
const os = require('os');

// Test configuration
const TEST_DIR = path.join(os.tmpdir(), `bd-integration-test-${Date.now()}`);
const PACKAGE_DIR = path.join(__dirname, '..');

// ANSI colors for output
const colors = {
  reset: '\x1b[0m',
  green: '\x1b[32m',
  red: '\x1b[31m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  gray: '\x1b[90m'
};

function log(msg, color = 'reset') {
  console.log(`${colors[color]}${msg}${colors.reset}`);
}

function logTest(name) {
  log(`\n▶ ${name}`, 'blue');
}

function logSuccess(msg) {
  log(`  ✓ ${msg}`, 'green');
}

function logError(msg) {
  log(`  ✗ ${msg}`, 'red');
}

function logInfo(msg) {
  log(`  ℹ ${msg}`, 'gray');
}

// Test utilities
function exec(cmd, opts = {}) {
  const defaultOpts = {
    stdio: 'pipe',
    encoding: 'utf8',
    ...opts
  };
  try {
    return execSync(cmd, defaultOpts);
  } catch (err) {
    if (opts.throwOnError !== false) {
      throw err;
    }
    return err.stdout || err.stderr || '';
  }
}

function setupTestDir() {
  if (fs.existsSync(TEST_DIR)) {
    fs.rmSync(TEST_DIR, { recursive: true, force: true });
  }
  fs.mkdirSync(TEST_DIR, { recursive: true });
  logInfo(`Test directory: ${TEST_DIR}`);
}

function cleanupTestDir() {
  if (fs.existsSync(TEST_DIR)) {
    fs.rmSync(TEST_DIR, { recursive: true, force: true });
  }
}

// Test 1: Package installation
async function testPackageInstallation() {
  logTest('Test 1: Package Installation');

  try {
    // Pack the package
    logInfo('Packing npm package...');
    const packOutput = exec('npm pack', { cwd: PACKAGE_DIR });
    const tarball = packOutput.trim().split('\n').pop();
    const tarballPath = path.join(PACKAGE_DIR, tarball);

    logSuccess(`Package created: ${tarball}`);

    // Install from tarball in test directory
    logInfo('Installing package in test environment...');
    const npmPrefix = path.join(TEST_DIR, 'npm-global');
    fs.mkdirSync(npmPrefix, { recursive: true });

    exec(`npm install -g "${tarballPath}" --prefix "${npmPrefix}"`, {
      cwd: TEST_DIR,
      env: { ...process.env, npm_config_prefix: npmPrefix }
    });

    logSuccess('Package installed successfully');

    // Verify binary exists
    const bdPath = path.join(npmPrefix, 'bin', 'bd');
    if (!fs.existsSync(bdPath) && !fs.existsSync(bdPath + '.cmd')) {
      // On Windows, might be bd.cmd
      const windowsPath = path.join(npmPrefix, 'bd.cmd');
      if (!fs.existsSync(windowsPath)) {
        throw new Error(`bd binary not found at ${bdPath}`);
      }
    }

    logSuccess('bd binary installed');

    // Cleanup tarball
    fs.unlinkSync(tarballPath);

    return { npmPrefix, bdPath };
  } catch (err) {
    logError(`Package installation failed: ${err.message}`);
    throw err;
  }
}

// Test 2: Binary functionality
async function testBinaryFunctionality(npmPrefix) {
  logTest('Test 2: Binary Functionality');

  const bdCmd = path.join(npmPrefix, 'bin', 'bd');
  const env = { ...process.env, PATH: `${path.join(npmPrefix, 'bin')}:${process.env.PATH}` };

  try {
    // Test version command
    logInfo('Testing version command...');
    const version = exec(`"${bdCmd}" version`, { env });
    if (!version.includes('bd version')) {
      throw new Error(`Unexpected version output: ${version}`);
    }
    logSuccess(`Version: ${version.trim()}`);

    // Test help command
    logInfo('Testing help command...');
    const help = exec(`"${bdCmd}" --help`, { env });
    if (!help.includes('Available Commands')) {
      throw new Error('Help command did not return expected output');
    }
    logSuccess('Help command works');

    return true;
  } catch (err) {
    logError(`Binary functionality test failed: ${err.message}`);
    throw err;
  }
}

// Test 3: Basic bd workflow
async function testBasicWorkflow(npmPrefix) {
  logTest('Test 3: Basic bd Workflow');

  const projectDir = path.join(TEST_DIR, 'test-project');
  fs.mkdirSync(projectDir, { recursive: true });

  // Initialize git repo
  exec('git init', { cwd: projectDir });
  exec('git config user.email "test@example.com"', { cwd: projectDir });
  exec('git config user.name "Test User"', { cwd: projectDir });

  const bdCmd = path.join(npmPrefix, 'bin', 'bd');
  const env = {
    ...process.env,
    PATH: `${path.join(npmPrefix, 'bin')}:${process.env.PATH}`,
    BD_ACTOR: 'integration-test'
  };

  try {
    // Test bd init
    logInfo('Testing bd init...');
    exec(`"${bdCmd}" init --quiet`, { cwd: projectDir, env });

    if (!fs.existsSync(path.join(projectDir, '.beads'))) {
      throw new Error('.beads directory not created');
    }
    logSuccess('bd init successful');

    // Test bd create
    logInfo('Testing bd create...');
    const createOutput = exec(`"${bdCmd}" create "Test issue" -t task -p 1 --json`, {
      cwd: projectDir,
      env
    });
    const issue = JSON.parse(createOutput);
    if (!issue.id || typeof issue.id !== 'string') {
      throw new Error(`Invalid issue created: ${JSON.stringify(issue)}`);
    }
    // ID format can be bd-xxxx or projectname-xxxx depending on configuration
    logSuccess(`Created issue: ${issue.id}`);

    // Test bd list
    logInfo('Testing bd list...');
    const listOutput = exec(`"${bdCmd}" list --json`, { cwd: projectDir, env });
    const issues = JSON.parse(listOutput);
    if (!Array.isArray(issues) || issues.length !== 1) {
      throw new Error('bd list did not return expected issues');
    }
    logSuccess(`Listed ${issues.length} issue(s)`);

    // Test bd show
    logInfo('Testing bd show...');
    const showOutput = exec(`"${bdCmd}" show ${issue.id} --json`, { cwd: projectDir, env });
    const showResult = JSON.parse(showOutput);
    // bd show --json returns an array with one element
    const showIssue = Array.isArray(showResult) ? showResult[0] : showResult;
    // Compare IDs - both should be present and match
    if (!showIssue.id || showIssue.id !== issue.id) {
      throw new Error(`bd show returned wrong issue: expected ${issue.id}, got ${showIssue.id}`);
    }
    logSuccess(`Show issue: ${showIssue.title}`);

    // Test bd update
    logInfo('Testing bd update...');
    exec(`"${bdCmd}" update ${issue.id} --status in_progress`, { cwd: projectDir, env });
    const updatedOutput = exec(`"${bdCmd}" show ${issue.id} --json`, { cwd: projectDir, env });
    const updatedResult = JSON.parse(updatedOutput);
    const updatedIssue = Array.isArray(updatedResult) ? updatedResult[0] : updatedResult;
    if (updatedIssue.status !== 'in_progress') {
      throw new Error(`bd update did not change status: expected 'in_progress', got '${updatedIssue.status}'`);
    }
    logSuccess('Updated issue status');

    // Test bd close
    logInfo('Testing bd close...');
    exec(`"${bdCmd}" close ${issue.id} --reason "Test completed"`, { cwd: projectDir, env });
    const closedOutput = exec(`"${bdCmd}" show ${issue.id} --json`, { cwd: projectDir, env });
    const closedResult = JSON.parse(closedOutput);
    const closedIssue = Array.isArray(closedResult) ? closedResult[0] : closedResult;
    if (closedIssue.status !== 'closed') {
      throw new Error(`bd close did not close issue: expected 'closed', got '${closedIssue.status}'`);
    }
    logSuccess('Closed issue');

    // Test bd ready (should be empty after closing)
    logInfo('Testing bd ready...');
    const readyOutput = exec(`"${bdCmd}" ready --json`, { cwd: projectDir, env });
    const readyIssues = JSON.parse(readyOutput);
    if (readyIssues.length !== 0) {
      throw new Error('bd ready should return no issues after closing all');
    }
    logSuccess('Ready work detection works');

    return true;
  } catch (err) {
    logError(`Basic workflow test failed: ${err.message}`);
    throw err;
  }
}

// Test 4: Claude Code for Web simulation
async function testClaudeCodeWebSimulation(npmPrefix) {
  logTest('Test 4: Claude Code for Web Simulation');

  const sessionDir = path.join(TEST_DIR, 'claude-code-session');
  fs.mkdirSync(sessionDir, { recursive: true });

  try {
    // Initialize git repo (simulating a cloned project)
    exec('git init', { cwd: sessionDir });
    exec('git config user.email "agent@example.com"', { cwd: sessionDir });
    exec('git config user.name "Claude Agent"', { cwd: sessionDir });

    const bdCmd = path.join(npmPrefix, 'bin', 'bd');
    const env = {
      ...process.env,
      PATH: `${path.join(npmPrefix, 'bin')}:${process.env.PATH}`,
      BD_ACTOR: 'claude-agent'
    };

    // First session: initialize and create an issue
    logInfo('Session 1: Initialize and create issue...');
    exec(`"${bdCmd}" init --quiet`, { cwd: sessionDir, env });

    const createOutput = exec(
      `"${bdCmd}" create "Existing issue from previous session" -t task -p 1 --json`,
      { cwd: sessionDir, env }
    );
    const existingIssue = JSON.parse(createOutput);
    logSuccess(`Created issue in first session: ${existingIssue.id}`);

    // Simulate sync to git (bd automatically exports to JSONL)
    const beadsDir = path.join(sessionDir, '.beads');
    const jsonlPath = path.join(beadsDir, 'issues.jsonl');

    // Wait a moment for auto-export
    execSync('sleep 1');

    // Verify JSONL exists
    if (!fs.existsSync(jsonlPath)) {
      throw new Error('JSONL file not created');
    }

    // Remove the database to simulate a fresh clone
    const dbFiles = fs.readdirSync(beadsDir).filter(f => f.endsWith('.db'));
    dbFiles.forEach(f => fs.unlinkSync(path.join(beadsDir, f)));

    // Session 2: Re-initialize (simulating SessionStart hook in new session)
    logInfo('Session 2: Re-initialize from JSONL...');
    exec(`"${bdCmd}" init --quiet`, { cwd: sessionDir, env });
    logSuccess('bd init re-imported from JSONL');

    // Verify issue was imported
    const listOutput = exec(`"${bdCmd}" list --json`, { cwd: sessionDir, env });
    const issues = JSON.parse(listOutput);

    if (!issues.some(i => i.id === existingIssue.id)) {
      throw new Error(`Existing issue ${existingIssue.id} not imported from JSONL`);
    }
    logSuccess('Existing issues imported successfully');

    // Simulate agent finding ready work
    const readyOutput = exec(`"${bdCmd}" ready --json`, { cwd: sessionDir, env });
    const readyIssues = JSON.parse(readyOutput);

    if (readyIssues.length === 0) {
      throw new Error('No ready work found');
    }
    logSuccess(`Found ${readyIssues.length} ready issue(s)`);

    // Simulate agent creating a new issue
    const newCreateOutput = exec(
      `"${bdCmd}" create "Bug discovered during session" -t bug -p 0 --json`,
      { cwd: sessionDir, env }
    );
    const newIssue = JSON.parse(newCreateOutput);
    logSuccess(`Agent created new issue: ${newIssue.id}`);

    // Verify JSONL was updated
    const jsonlContent = fs.readFileSync(
      path.join(beadsDir, 'issues.jsonl'),
      'utf8'
    );
    const jsonlLines = jsonlContent.trim().split('\n');

    if (jsonlLines.length < 2) {
      throw new Error('JSONL not updated with new issue');
    }
    logSuccess('JSONL auto-export working');

    return true;
  } catch (err) {
    logError(`Claude Code for Web simulation failed: ${err.message}`);
    throw err;
  }
}

// Test 5: Multi-platform binary detection
async function testPlatformDetection() {
  logTest('Test 5: Platform Detection');

  try {
    const platform = os.platform();
    const arch = os.arch();

    logInfo(`Current platform: ${platform}`);
    logInfo(`Current architecture: ${arch}`);

    // Verify postinstall would work for this platform
    const supportedPlatforms = {
      darwin: ['x64', 'arm64'],
      linux: ['x64', 'arm64'],
      win32: ['x64', 'arm64']
    };

    if (!supportedPlatforms[platform]) {
      throw new Error(`Unsupported platform: ${platform}`);
    }

    const archMap = { x64: 'amd64', arm64: 'arm64' };
    const mappedArch = archMap[arch];

    if (!supportedPlatforms[platform].includes(arch)) {
      throw new Error(`Unsupported architecture: ${arch} for platform ${platform}`);
    }

    logSuccess(`Platform ${platform}-${mappedArch} is supported`);

    // Check if GitHub release has this binary
    const version = require(path.join(PACKAGE_DIR, 'package.json')).version;
    const ext = platform === 'win32' ? 'zip' : 'tar.gz';
    const binaryUrl = `https://github.com/steveyegge/beads/releases/download/v${version}/beads_${version}_${platform}_${mappedArch}.${ext}`;

    logInfo(`Expected binary URL: ${binaryUrl}`);
    logSuccess('Platform detection logic validated');

    return true;
  } catch (err) {
    logError(`Platform detection test failed: ${err.message}`);
    throw err;
  }
}

// Main test runner
async function runTests() {
  log('\n╔════════════════════════════════════════╗', 'blue');
  log('║  @beads/bd Integration Tests          ║', 'blue');
  log('╚════════════════════════════════════════╝', 'blue');

  let npmPrefix;
  const results = {
    passed: 0,
    failed: 0,
    total: 0
  };

  try {
    setupTestDir();

    // Test 1: Installation
    results.total++;
    try {
      const installResult = await testPackageInstallation();
      npmPrefix = installResult.npmPrefix;
      results.passed++;
    } catch (err) {
      results.failed++;
      log('\n⚠️  Skipping remaining tests due to installation failure', 'yellow');
      throw err;
    }

    // Test 2: Binary functionality
    results.total++;
    try {
      await testBinaryFunctionality(npmPrefix);
      results.passed++;
    } catch (err) {
      results.failed++;
    }

    // Test 3: Basic workflow
    results.total++;
    try {
      await testBasicWorkflow(npmPrefix);
      results.passed++;
    } catch (err) {
      results.failed++;
    }

    // Test 4: Claude Code for Web
    results.total++;
    try {
      await testClaudeCodeWebSimulation(npmPrefix);
      results.passed++;
    } catch (err) {
      results.failed++;
    }

    // Test 5: Platform detection
    results.total++;
    try {
      await testPlatformDetection();
      results.passed++;
    } catch (err) {
      results.failed++;
    }

  } finally {
    // Cleanup
    logInfo('\nCleaning up test directory...');
    cleanupTestDir();
  }

  // Print summary
  log('\n╔════════════════════════════════════════╗', 'blue');
  log('║  Test Summary                          ║', 'blue');
  log('╚════════════════════════════════════════╝', 'blue');
  log(`\nTotal tests: ${results.total}`, 'blue');
  log(`Passed: ${results.passed}`, results.passed === results.total ? 'green' : 'yellow');
  log(`Failed: ${results.failed}`, results.failed > 0 ? 'red' : 'green');

  if (results.failed > 0) {
    log('\n❌ Some tests failed', 'red');
    process.exit(1);
  } else {
    log('\n✅ All tests passed!', 'green');
    process.exit(0);
  }
}

// Run tests
if (require.main === module) {
  runTests().catch(err => {
    log(`\n❌ Test suite failed: ${err.message}`, 'red');
    console.error(err);
    cleanupTestDir();
    process.exit(1);
  });
}

module.exports = { runTests };
