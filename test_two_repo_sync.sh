#!/bin/bash
# Test daemon auto-import after git pull (bd-09b5f2f5)
# This verifies the critical data corruption fix

set -e

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

echo "=== Setting up test environment ==="
cd $TMPDIR

# Create origin repo
mkdir origin && cd origin
git init --bare

cd $TMPDIR

# Clone repo A
git clone origin repoA
cd repoA
git config user.name "Test User A"
git config user.email "test-a@example.com"

# Initialize bd in repo A
echo "=== Initializing bd in repo A ==="
bd init --prefix test --quiet
bd create "Initial issue" -p 1 -d "Created in repo A" --json

# Commit and push (use master as default branch)
git add .
git commit -m "Initial commit with bd"
git push origin master

cd $TMPDIR

# Clone repo B
echo "=== Cloning repo B ==="
git clone origin repoB
cd repoB
git config user.name "Test User B"
git config user.email "test-b@example.com"

# Initialize bd in repo B (import from JSONL)
echo "=== Initializing bd in repo B ==="
bd init --prefix test --quiet

# Verify repo B can read the issue
echo "=== Verifying repo B sees initial issue ==="
ISSUE_ID=$(bd list --json | jq -r '.[0].id')
echo "Found issue: $ISSUE_ID"

# In repo A: Update the issue
cd $TMPDIR/repoA
echo "=== Repo A: Updating issue status ==="
bd update $ISSUE_ID --status in_progress --json
bd sync  # Force immediate export/commit/push

# Wait for export to flush
sleep 2

# In repo B: Pull and verify daemon auto-imports
cd $TMPDIR/repoB
echo "=== Repo B: Pulling changes ==="
git pull

# Check if daemon auto-imports (should see updated status)
echo "=== Repo B: Checking if daemon auto-imported ==="
STATUS=$(bd show $ISSUE_ID --json | jq -r '.[0].status')

if [ "$STATUS" == "in_progress" ]; then
    echo "✅ SUCCESS: Daemon auto-imported! Status is 'in_progress' as expected"
    exit 0
else
    echo "❌ FAIL: Daemon did NOT auto-import. Status is '$STATUS', expected 'in_progress'"
    echo ""
    echo "This indicates bd-09b5f2f5 regression - daemon serving stale data"
    exit 1
fi
