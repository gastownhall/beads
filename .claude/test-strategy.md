# Test Running Strategy for Claude Code

## Critical Rules

1. **ALWAYS use `./scripts/test.sh` instead of `go test` directly**
   - It automatically skips broken tests from `.test-skip`
   - Uses appropriate timeouts (3m default)
   - Consistent with human developers and CI/CD

2. **Use `-run` to target specific tests when developing features**
   ```bash
   # Good: When working on feature X
   ./scripts/test.sh -run TestFeatureX ./cmd/bd/...

   # Avoid: Running full suite unnecessarily
   ./scripts/test.sh ./...
   ```

3. **Understand the bottleneck: COMPILATION not EXECUTION**
   - 180s compilation time vs 3.8s actual test execution (cmd/bd)
   - Running subset of tests doesn't save much time (still recompiles)
   - But use `-run` anyway to avoid seeing unrelated failures

## Common Commands

```bash
# Full test suite (what 'make test' runs)
./scripts/test.sh

# Test specific package
./scripts/test.sh ./cmd/bd/...
./scripts/test.sh ./internal/storage/sqlite/...

# Test specific feature
./scripts/test.sh -run TestCreate ./cmd/bd/...
./scripts/test.sh -run TestImport

# Verbose output (when debugging)
./scripts/test.sh -v -run TestSpecificTest
```

## When Tests Fail

1. **Check if it's a known broken test:**
   ```bash
   cat .test-skip
   ```

2. **If it's new, investigate:**
   - Read the test failure message
   - Run with `-v` for more detail
   - Check if recent code changes broke it

3. **If unfixable now:**
   - File GitHub issue with details
   - Add to `.test-skip` with issue reference
   - Document in commit message

## Package Size Context

The `cmd/bd` package is LARGE:
- 41,696 lines of code
- 205 files (82 test files)
- 313 individual tests
- Compilation takes ~180 seconds

This is why:
- Compilation is slow
- Test script uses 3-minute timeout
- Targeting specific tests is important

## Environment Variables

Use these when needed:

```bash
# Custom timeout
TEST_TIMEOUT=5m ./scripts/test.sh

# Verbose by default
TEST_VERBOSE=1 ./scripts/test.sh

# Run pattern
TEST_RUN=TestSomething ./scripts/test.sh
```

## Quick Reference

| Task | Command |
|------|---------|
| Run all tests | `make test` or `./scripts/test.sh` |
| Test one package | `./scripts/test.sh ./cmd/bd/...` |
| Test one function | `./scripts/test.sh -run TestName` |
| Verbose output | `./scripts/test.sh -v` |
| Custom timeout | `./scripts/test.sh -timeout 10m` |
| Skip additional test | `./scripts/test.sh -skip TestFoo` |

## Remember

- The test script is in `.gitignore` path: `scripts/test.sh`
- Skip list is in repo root: `.test-skip`
- Full documentation: `docs/TESTING.md`
- Current broken tests: See GH issues #355, #356
