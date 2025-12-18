# Integration Tests

This directory contains integration tests for bd (beads) that test end-to-end functionality.

## Prerequisites

- bd installed: `go install github.com/steveyegge/beads/cmd/bd@latest`
- Python 3.7+ for Python-based tests

## Running Tests

```bash
# Run all integration tests
python3 -m pytest tests/integration/
```

## Adding New Tests

Integration tests should:
1. Use temporary workspaces (cleaned up automatically)
2. Test real bd CLI commands, not just internal APIs
3. Use `--no-daemon` flag for fast execution
4. Verify behavior in `.beads/issues.jsonl` when relevant
5. Clean up resources in `finally` blocks
6. Provide clear output showing what's being tested
