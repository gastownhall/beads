# Testing Philosophy

This document covers **what to test** and **what not to test**. For how to run tests, see [TESTING.md](TESTING.md).

## The Test Pyramid

```
                  ┌─────────────────┐
                  │   E2E Tests     │  ← PR/Deploy only (slow, expensive)
                  │   ~5% of tests  │
                  └────────┬────────┘
                           │
            ┌──────────────┴──────────────┐
            │     Integration Tests       │  ← PR gate (moderate)
            │       ~15% of tests         │
            └──────────────┬──────────────┘
                           │
  ┌────────────────────────┴────────────────────────┐
  │              Unit Tests (Fast)                  │  ← Every save/commit
  │                 ~80% of tests                   │
  └─────────────────────────────────────────────────┘
```

### Tier 1: Fast Tests (< 5 seconds total)

**When**: Every file save, pre-commit hooks, continuous during development

- Pure function tests (no I/O)
- In-memory data structure tests
- Business logic validation
- Mock all external dependencies

**In beads**: Core logic tests using `newTestStore()` with in-memory SQLite

### Tier 2: Integration Tests (< 30 seconds)

**When**: Pre-push, PR checks

- Real file system operations
- Git operations with temp repos
- Config file parsing
- CLI argument handling

**In beads**: Tests tagged with `//go:build integration`, daemon tests

### Tier 3: E2E / Smoke Tests (1-5 minutes)

**When**: PR merge, pre-deploy, nightly

- Full `bd init` → `bd doctor` → `bd doctor --fix` workflow
- Real API calls (to staging)
- Cross-platform verification

---

## What Makes a Test "Right"

A good test:

1. **Catches a bug you'd actually ship** - not theoretical edge cases
2. **Documents expected behavior** - serves as living documentation
3. **Runs fast enough to not skip** - slow tests get disabled
4. **Isn't duplicated elsewhere** - tests one thing, one way

---

## What to Test (Priority Matrix)

| Priority | What | Why | Examples in beads |
|----------|------|-----|-------------------|
| **High** | Core business logic | This is what users depend on | `sync`, `doctor`, `export`, `import` |
| **High** | Error paths that could corrupt data | Data loss is catastrophic | Config handling, git operations, JSONL integrity |
| **Medium** | Edge cases from production bugs | Discovered through real issues | Orphan handling, ID collision detection |
| **Low** | Display/formatting | Visual output, can be manually verified | Table formatting, color output |

---

## What NOT to Test Extensively

### Simple utility functions
Trust the language. Don't test that `strings.TrimSpace` works.

### Every permutation of inputs
Use table-driven tests with representative cases instead of exhaustive permutations.

```go
// BAD: 10 separate test functions
func TestPriority0(t *testing.T) { ... }
func TestPriority1(t *testing.T) { ... }
func TestPriority2(t *testing.T) { ... }

// GOOD: One table-driven test
func TestPriorityMapping(t *testing.T) {
    cases := []struct{ in, want int }{
        {0, 4}, {1, 0}, {5, 3}, // includes boundary
    }
    for _, tc := range cases {
        t.Run(fmt.Sprintf("priority_%d", tc.in), func(t *testing.T) {
            got := mapPriority(tc.in)
            if got != tc.want { t.Errorf(...) }
        })
    }
}
```

### Obvious behavior
Don't test "if file exists, return true" - trust the implementation.

### Same logic through different entry points
If you test a function directly, don't also test it through every caller.

---

## Anti-Patterns to Avoid

### 1. Trivial Assertions

Testing obvious happy paths that would pass with trivial implementations.

```go
// BAD: What bug would this catch?
func TestValidateBeadsWorkspace(t *testing.T) {
    dir := setupTestWorkspace(t)
    if err := validateBeadsWorkspace(dir); err != nil {
        t.Errorf("expected no error, got: %v", err)
    }
}

// GOOD: Test the interesting error cases
func TestValidateBeadsWorkspace(t *testing.T) {
    cases := []struct{
        name    string
        setup   func(t *testing.T) string
        wantErr string
    }{
        {"missing .beads dir", setupNoBeadsDir, "not a beads workspace"},
        {"corrupted db", setupCorruptDB, "database is corrupted"},
        {"permission denied", setupNoReadAccess, "permission denied"},
    }
    // ...
}
```

### 2. Duplicate Error Path Testing

Testing the same logic multiple ways instead of once with table-driven tests.

```go
// BAD: Repetitive individual assertions
if config.PriorityMap["0"] != 4 { t.Errorf(...) }
if config.PriorityMap["1"] != 0 { t.Errorf(...) }
if config.PriorityMap["2"] != 1 { t.Errorf(...) }

// GOOD: Table-driven
for k, want := range expectedMap {
    if got := config.PriorityMap[k]; got != want {
        t.Errorf("PriorityMap[%q] = %d, want %d", k, got, want)
    }
}
```

### 3. I/O Heavy Tests Without Mocking

Unit tests that execute real commands or heavy I/O when they could mock.

```go
// BAD: Actually executes bd killall in unit test
func TestDaemonFix(t *testing.T) {
    exec.Command("bd", "killall").Run()
    // ...
}

// GOOD: Mock the execution or use integration test tag
func TestDaemonFix(t *testing.T) {
    executor := &mockExecutor{}
    fix := NewDaemonFix(executor)
    // ...
}
```

### 4. Testing Implementation, Not Behavior

Tests that break when you refactor, even though behavior is unchanged.

```go
// BAD: Tests internal state
if len(daemon.connectionPool) != 3 { t.Error(...) }

// GOOD: Tests observable behavior
if resp, err := daemon.HandleRequest(req); err != nil { t.Error(...) }
```

### 5. Missing Boundary Tests

Testing known-good values but not boundaries and invalid inputs.

```go
// BAD: Only tests middle values
TestPriority(1)  // works
TestPriority(2)  // works

// GOOD: Tests boundaries and invalid
TestPriority(-1) // invalid - expect error
TestPriority(0)  // boundary - min valid
TestPriority(4)  // boundary - max valid
TestPriority(5)  // boundary - first invalid
```

---

## Target Metrics

| Metric | Target | Current (beads) | Status |
|--------|--------|-----------------|--------|
| Test-to-code ratio | 0.5:1 - 1.5:1 | 0.85:1 | Healthy |
| Fast test suite | < 5 seconds | 3.8 seconds | Good |
| Integration tests | < 30 seconds | ~15 seconds | Good |
| Compilation overhead | Minimize | 180 seconds | Bottleneck |

### Interpretation

- **0.5:1** - Light coverage, fast iteration (acceptable for utilities)
- **1:1** - Solid coverage for most projects (our target)
- **1.5:1** - Heavy coverage for critical systems
- **2:1+** - Over-engineered, maintenance burden

---

## Beads-Specific Guidance

### Well-Covered (Maintain)

| Area | Why It's Well-Tested |
|------|---------------------|
| Sync/Export/Import | Data integrity critical - comprehensive edge cases |
| SQLite transactions | Rollback safety, atomicity guarantees |
| Merge operations | 3-way merge with conflict resolution |
| Daemon locking | Prevents corruption from multiple instances |

### Needs Attention

| Area | Gap | Priority |
|------|-----|----------|
| Daemon lifecycle | Shutdown/signal handling | Medium |
| Concurrent operations | Stress testing under load | Medium |
| Boundary validation | Edge inputs in mapping functions | Low |

### Skip These

- Display formatting tests (manually verify)
- Simple getters/setters
- Tests that duplicate SQLite's guarantees

---

## Related Docs

- [TESTING.md](TESTING.md) - How to run tests
- [README_TESTING.md](README_TESTING.md) - Fast vs integration test strategy
- [dev-notes/TEST_SUITE_AUDIT.md](dev-notes/TEST_SUITE_AUDIT.md) - Test refactoring progress
