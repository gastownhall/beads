# Integration Tests

This directory contains integration tests for bd (beads) that test end-to-end functionality.

## Tests

### test_agent_race.py

Multi-agent race condition test that validates collision prevention with Agent Mail.

**What it tests:**
- Multiple agents simultaneously attempting to claim the same issue
- WITH Agent Mail: Only one agent succeeds (via reservation)
- WITHOUT Agent Mail: Multiple agents may succeed (collision)
- Verification via JSONL that no duplicate claims occur

**Prerequisites:**
- bd installed: `go install github.com/steveyegge/beads/cmd/bd@latest`
- Agent Mail server running (optional, for full test suite):
  ```bash
  cd ~/src/mcp_agent_mail
  source .venv/bin/activate
  uv run python -m mcp_agent_mail.cli serve-http
  ```

**Running:**
```bash
python3 tests/integration/test_agent_race.py
```

**Expected results:**
- **WITH Agent Mail running:** Test 1 passes (only 1 claim), Test 2 shows collision, Test 3 passes
- **WITHOUT Agent Mail running:** All tests demonstrate collision (expected behavior without reservation system)

## Adding New Tests

Integration tests should:
1. Use temporary workspaces (cleaned up automatically)
2. Test real bd CLI commands, not just internal APIs
3. Verify behavior in `.beads/issues.jsonl` when relevant
4. Clean up resources in `finally` blocks
5. Provide clear output showing what's being tested
