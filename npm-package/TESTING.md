# Testing the @beads/bd npm Package

This document describes the testing strategy and how to run tests for the @beads/bd npm package.

## Test Suites

### 1. Unit Tests (`npm test`)

**Location**: `scripts/test.js`

**Purpose**: Quick smoke tests to verify basic installation

**Tests**:
- Binary version check
- Help command

**Run**:
```bash
npm test
```

**Duration**: <1 second

### 2. Integration Tests (`npm run test:integration`)

**Location**: `test/integration.test.js`

**Purpose**: Comprehensive end-to-end testing of the npm package

**Tests**:

#### Test 1: Package Installation
- Packs the npm package into a tarball
- Installs globally in an isolated test environment
- Verifies binary is downloaded and installed correctly

#### Test 2: Binary Functionality
- Tests `bd version` command
- Tests `bd --help` command
- Verifies native binary works through Node wrapper

#### Test 3: Basic bd Workflow
- Creates test project with git
- Runs `bd init --quiet`
- Creates an issue with `bd create`
- Lists issues with `bd list --json`
- Shows issue details with `bd show`
- Updates issue status with `bd update`
- Closes issue with `bd close`
- Verifies ready work detection with `bd ready`

#### Test 4: Claude Code for Web Simulation
- **Session 1**: Initializes bd, creates an issue
- Verifies JSONL export
- Deletes database to simulate fresh clone
- **Session 2**: Re-initializes from JSONL (simulates SessionStart hook)
- Verifies issues are imported from JSONL
- Creates new issue (simulating agent discovery)
- Verifies JSONL auto-export works

#### Test 5: Platform Detection
- Verifies current platform is supported
- Validates binary URL construction
- Confirms GitHub release has required binaries

**Run**:
```bash
npm run test:integration
```

**Duration**: ~30-60 seconds (downloads binaries)

### 3. All Tests (`npm run test:all`)

Runs both unit and integration tests sequentially.

```bash
npm run test:all
```

## Test Results

All tests passing:

```
╔════════════════════════════════════════╗
║  Test Summary                          ║
╚════════════════════════════════════════╝

Total tests: 5
Passed: 5
Failed: 0

✅ All tests passed!
```

## What the Tests Verify

### Package Installation
- ✅ npm pack creates valid tarball
- ✅ npm install downloads and installs package
- ✅ Postinstall script runs automatically
- ✅ Platform-specific binary is downloaded
- ✅ Binary is extracted correctly
- ✅ Binary is executable

### Binary Functionality
- ✅ CLI wrapper invokes native binary
- ✅ All arguments pass through correctly
- ✅ Exit codes propagate
- ✅ stdio streams work (stdin/stdout/stderr)

### bd Commands
- ✅ `bd init` creates .beads directory
- ✅ `bd create` creates issues with hash IDs
- ✅ `bd list` returns JSON array
- ✅ `bd show` returns issue details
- ✅ `bd update` modifies issue status
- ✅ `bd close` closes issues
- ✅ `bd ready` finds work with no blockers

### Claude Code for Web Use Case
- ✅ Fresh installation works
- ✅ JSONL export happens automatically
- ✅ Database can be recreated from JSONL
- ✅ Issues survive database deletion
- ✅ SessionStart hook pattern works
- ✅ Agent can create new issues
- ✅ Auto-sync keeps JSONL updated

### Platform Support
- ✅ macOS (darwin) - amd64, arm64
- ✅ Linux - amd64, arm64
- ✅ Windows - amd64 (zip format)
- ✅ Correct binary URLs generated
- ✅ GitHub releases have required assets

## Testing Before Publishing

Before publishing a new version to npm:

```bash
# 1. Update version in package.json
npm version patch  # or minor/major

# 2. Run all tests
npm run test:all

# 3. Test installation from local tarball
npm pack
npm install -g ./beads-bd-X.Y.Z.tgz
bd version

# 4. Verify in a fresh project
mkdir /tmp/test-bd
cd /tmp/test-bd
git init
bd init
bd create "Test" -p 1
bd list

# 5. Cleanup
npm uninstall -g @beads/bd
```

## Continuous Integration

### GitHub Actions (Recommended)

Create `.github/workflows/test-npm-package.yml`:

```yaml
name: Test npm Package
on:
  push:
    paths:
      - 'npm-package/**'
  pull_request:
    paths:
      - 'npm-package/**'

jobs:
  test:
    runs-on: ${{ matrix.os }}
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
        node-version: [18, 20]

    steps:
      - uses: actions/checkout@v3

      - name: Setup Node.js
        uses: actions/setup-node@v3
        with:
          node-version: ${{ matrix.node-version }}

      - name: Run unit tests
        run: |
          cd npm-package
          npm test

      - name: Run integration tests
        run: |
          cd npm-package
          npm run test:integration
```

## Manual Testing Scenarios

### Scenario 1: Claude Code for Web SessionStart Hook

1. Create `.claude/hooks/session-start.sh`:
   ```bash
   #!/bin/bash
   npm install -g @beads/bd
   bd init --quiet
   ```

2. Make executable: `chmod +x .claude/hooks/session-start.sh`

3. Start new Claude Code for Web session

4. Verify:
   ```bash
   bd version  # Should work
   bd list     # Should show existing issues
   ```

### Scenario 2: Global Installation

```bash
# Install globally
npm install -g @beads/bd

# Verify
which bd
bd version

# Use in any project
mkdir ~/projects/test
cd ~/projects/test
git init
bd init
bd create "First issue" -p 1
bd list
```

### Scenario 3: Project Dependency

```bash
# Add to project
npm install --save-dev @beads/bd

# Use via npx
npx bd version
npx bd init
npx bd create "Issue" -p 1
```

### Scenario 4: Offline/Cached Installation

```bash
# First install (downloads binary)
npm install -g @beads/bd

# Uninstall
npm uninstall -g @beads/bd

# Reinstall (should use npm cache)
npm install -g @beads/bd
# Should be faster (no binary download if cached)
```

## Troubleshooting Tests

### Test fails with "binary not found"

**Cause**: Postinstall script didn't download binary

**Fix**:
- Check GitHub release has required binaries
- Verify package.json version matches release
- Check network connectivity

### Test fails with "permission denied"

**Cause**: Binary not executable

**Fix**:
- Postinstall should chmod +x on Unix
- Windows doesn't need this

### Integration test times out

**Cause**: Network slow, binary download taking too long

**Fix**:
- Increase timeout in test
- Use cached npm packages
- Run on faster network

### JSONL import test fails

**Cause**: Database format changed or JSONL format incorrect

**Fix**:
- Check bd version compatibility
- Verify JSONL format matches current schema
- Update test to use proper operation records

## Test Coverage

| Area | Coverage |
|------|----------|
| Package installation | ✅ Full |
| Binary download | ✅ Full |
| CLI wrapper | ✅ Full |
| Basic commands | ✅ High (8 commands) |
| JSONL sync | ✅ Full |
| Platform detection | ✅ Full |
| Error handling | ⚠️ Partial |
| MCP server | ❌ Not included |

## Known Limitations

1. **No MCP server tests**: The npm package only includes the CLI binary, not the Python MCP server
2. **Platform testing**: Tests only run on the current platform (need CI for full coverage)
3. **Network dependency**: Integration tests require internet to download binaries
4. **Timing sensitivity**: JSONL auto-export has 5-second debounce, tests use sleep

## Future Improvements

1. **Mock binary downloads** for faster tests
2. **Cross-platform CI** to test on all OSes
3. **MCP server integration** (if Node.js MCP server is added)
4. **Performance benchmarks** for binary download times
5. **Stress testing** with many issues
6. **Concurrent operation testing** for race conditions

## FAQ

**Q: Do I need to run tests before every commit?**
A: Run `npm test` (quick unit tests). Run full integration tests before publishing.

**Q: Why do integration tests take so long?**
A: They download ~17MB binary from GitHub releases. First run is slower.

**Q: Can I run tests offline?**
A: Unit tests yes, integration tests no (need to download binary).

**Q: Do tests work on Windows?**
A: Yes, but integration tests need PowerShell for zip extraction.

**Q: How do I test a specific version?**
A: Update package.json version, ensure GitHub release exists, run tests.
