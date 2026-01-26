#!/bin/bash
# Test script to reproduce GH#1224 - SQLite WAL mode on WSL2 bind mounts

set -e

echo "=== Testing SQLite WAL mode on WSL2 bind mounts ==="

# Check if we're in WSL2
if ! grep -qi microsoft /proc/version; then
    echo "Not running in WSL2, skipping test"
    exit 0
fi

# Create test directory in different locations
test_paths=(
    "/tmp/beads-test-wal"                    # /tmp (Linux filesystem)
    "/mnt/c/temp/beads-test-wal"             # Windows filesystem (if available)
)

# Add Docker Desktop bind mount if available
if [ -d "/mnt/wsl" ]; then
    test_paths+=("/mnt/wsl/beads-test-wal")  # Docker Desktop bind mount
fi

for test_path in "${test_paths[@]}"; do
    echo ""
    echo "Testing WAL mode at: $test_path"
    
    # Create directory
    mkdir -p "$test_path"
    
    # Test with SQLite CLI
    sqlite3 "$test_path/test.db" "PRAGMA journal_mode=WAL;" 2>&1 | head -1
    
    # Check if WAL was actually enabled
    journal_mode=$(sqlite3 "$test_path/test.db" "PRAGMA journal_mode;" 2>&1 | head -1)
    echo "Journal mode: $journal_mode"
    
    # Clean up
    rm -rf "$test_path"
done

echo ""
echo "=== Test complete ==="
