# Manual Test Plan: Dolt Git Remotes with GitHub

Validates that a standalone Beads Dolt database can push/pull/clone to a real
GitHub repository using Dolt v1.81.8+ native git remote support. Dolt stores
its data under `refs/dolt/data` in the git repo, invisible to normal `git clone`.

**Dolt version required:** 1.81.8+
**Estimated time:** 15-20 minutes

## Prerequisites

- [ ] `dolt version` shows >= 1.81.8
- [ ] SSH key configured for GitHub (`ssh -T git@github.com` succeeds)
- [ ] A **scratch GitHub repo** you can push to (public or private)
  - Create one at https://github.com/new (e.g., `beads-dolt-test`)
  - Can be empty (no README, no .gitignore)
- [ ] `bd` CLI built and available on PATH
- [ ] `git` CLI available

## Variables

Set these before starting. All commands below reference them:

```bash
GITHUB_REPO="git@github.com:<you>/beads-dolt-test.git"
WORKDIR=$(mktemp -d -t beads-git-remote-test)
echo "Working in: $WORKDIR"
```

---

## Phase 1: Initialize and Push

### 1.1 Create a Dolt-backed beads database

```bash
mkdir -p "$WORKDIR/town-a" && cd "$WORKDIR/town-a"
git init && git commit --allow-empty -m "init"
bd init --backend dolt
```

**Expected:** `.beads/dolt/` directory created, `bd list` returns empty.

- [ ] PASS / FAIL: `bd init --backend dolt` succeeds
- [ ] PASS / FAIL: `.beads/dolt/` directory exists
- [ ] PASS / FAIL: `bd list` runs without error

### 1.2 Create test data

```bash
bd create "Test issue alpha" -p 1
bd create "Test issue beta" -p 2 -t bug
bd create "Test issue gamma" -p 3
```

**Expected:** Three issues created.

- [ ] PASS / FAIL: `bd list` shows 3 issues with correct priorities/types

### 1.3 Add GitHub repo as a Dolt remote

```bash
cd .beads/dolt
dolt remote add origin "$GITHUB_REPO"
dolt remote -v
```

**Expected:** Remote `origin` listed pointing to the GitHub repo URL.

- [ ] PASS / FAIL: `dolt remote -v` shows origin with correct URL

### 1.4 Push to GitHub

```bash
cd "$WORKDIR/town-a/.beads/dolt"
dolt push -u origin main
```

**Expected:** Push succeeds. Dolt data is stored under `refs/dolt/data` in the
GitHub repo.

- [ ] PASS / FAIL: `dolt push` exits 0 with no errors

### 1.5 Verify data arrived on GitHub

```bash
git ls-remote "$GITHUB_REPO" 'refs/dolt/*'
```

**Expected:** At least one ref matching `refs/dolt/data` (or similar Dolt refs).

- [ ] PASS / FAIL: `git ls-remote` shows `refs/dolt/*` references

---

## Phase 2: Clone on Second Machine (Simulated)

### 2.1 Dolt clone from GitHub

```bash
mkdir -p "$WORKDIR/town-b" && cd "$WORKDIR/town-b"
dolt clone "$GITHUB_REPO" beads-clone
cd beads-clone
```

**Expected:** Dolt database cloned successfully.

- [ ] PASS / FAIL: `dolt clone` exits 0
- [ ] PASS / FAIL: `dolt log` shows commit history

### 2.2 Verify data integrity

```bash
dolt sql -q "SHOW TABLES;"
dolt sql -q "SELECT id, title, priority, type FROM issues ORDER BY priority;"
```

**Expected:** `issues` table present. All three test issues visible with
correct field values.

- [ ] PASS / FAIL: `SHOW TABLES` includes `issues`
- [ ] PASS / FAIL: All 3 issues present with correct title/priority/type

### 2.3 Verify round-trip of all tables

```bash
dolt sql -q "SHOW TABLES;"
```

Check that ALL tables from the original database are present (issues, labels,
dependencies, comments, events, metadata, etc.).

- [ ] PASS / FAIL: Table count matches original database

---

## Phase 3: Incremental Push and Pull

### 3.1 Add more data in town-a

```bash
cd "$WORKDIR/town-a"
bd create "Incremental issue delta" -p 1
cd .beads/dolt
dolt add .
dolt commit -m "Add delta issue"
dolt push origin main
```

**Expected:** Incremental push succeeds (not a full re-upload).

- [ ] PASS / FAIL: `dolt push` exits 0

### 3.2 Pull in town-b

```bash
cd "$WORKDIR/town-b/beads-clone"
dolt pull origin main
```

**Expected:** Pull succeeds, new issue appears.

- [ ] PASS / FAIL: `dolt pull` exits 0
- [ ] PASS / FAIL: `dolt sql -q "SELECT title FROM issues WHERE title LIKE '%delta%';"` returns the new issue

---

## Phase 4: Git Clone Isolation

### 4.1 Verify git clone does NOT fetch Dolt data

```bash
cd "$WORKDIR"
git clone "$GITHUB_REPO" git-only-clone
cd git-only-clone
```

**Expected:** Normal git clone succeeds but does NOT contain Dolt database
data. The `refs/dolt/data` refs are not fetched by default git clone.

```bash
git log --oneline          # Should show git commits (if any), not Dolt data
git show-ref | grep dolt   # Should return nothing or empty
ls                         # Should NOT contain Dolt database files
```

- [ ] PASS / FAIL: `git clone` succeeds
- [ ] PASS / FAIL: No `refs/dolt/*` in local refs (`git show-ref | grep dolt` is empty)
- [ ] PASS / FAIL: No Dolt database files in working tree

---

## Phase 5: Cleanup

### 5.1 Remove Dolt data from GitHub repo

```bash
cd "$WORKDIR"
git clone "$GITHUB_REPO" cleanup-clone
cd cleanup-clone
git push origin :refs/dolt/data
```

Or delete all dolt refs:

```bash
git ls-remote origin 'refs/dolt/*' | awk '{print $2}' | while read ref; do
  git push origin ":$ref"
done
```

**Expected:** Dolt refs removed from GitHub.

- [ ] PASS / FAIL: `git ls-remote "$GITHUB_REPO" 'refs/dolt/*'` returns empty

### 5.2 Delete scratch repo (optional)

Delete the GitHub repo via the web UI or:

```bash
gh repo delete <you>/beads-dolt-test --yes
```

### 5.3 Clean up local temp directory

```bash
rm -rf "$WORKDIR"
```

- [ ] PASS / FAIL: Temp directory removed

---

## Phase 6: Edge Cases (Optional)

### 6.1 Push to non-empty GitHub repo

Create a GitHub repo with a README, then try the push flow. Verify Dolt data
coexists with normal git content.

- [ ] PASS / FAIL: Dolt push succeeds to repo with existing git content
- [ ] PASS / FAIL: `git clone` still gets only the git content (README)

### 6.2 SSH vs HTTPS authentication

Repeat Phase 1.3-1.4 with HTTPS URL and a GitHub token:

```bash
GITHUB_REPO_HTTPS="https://github.com/<you>/beads-dolt-test.git"
dolt remote add origin-https "$GITHUB_REPO_HTTPS"
DOLT_REMOTE_PASSWORD="<github-pat>" dolt push --user "<you>" origin-https main
```

- [ ] PASS / FAIL: HTTPS push with token succeeds

### 6.3 Large dataset

Create 100+ issues, push, clone, and verify all data arrives intact:

```bash
for i in $(seq 1 100); do bd create "Bulk issue $i" -p $((i % 4 + 1)); done
```

- [ ] PASS / FAIL: All 100+ issues survive round-trip

### 6.4 Special characters in data

```bash
bd create 'Issue with "quotes" and <brackets> & ampersands'
bd create "Issue with unicode: emoji 🐛 and CJK 你好"
```

Push, clone, verify data integrity.

- [ ] PASS / FAIL: Special characters survive round-trip

---

## Summary Checklist

| # | Test | Result |
|---|------|--------|
| 1.1 | Init Dolt backend | |
| 1.2 | Create test data | |
| 1.3 | Add GitHub remote | |
| 1.4 | Push to GitHub | |
| 1.5 | Verify refs on GitHub | |
| 2.1 | Dolt clone from GitHub | |
| 2.2 | Data integrity check | |
| 2.3 | All tables round-trip | |
| 3.1 | Incremental push | |
| 3.2 | Pull new data | |
| 4.1 | Git clone isolation | |
| 5.1 | Cleanup Dolt refs | |
| 6.1 | Non-empty repo (optional) | |
| 6.2 | HTTPS auth (optional) | |
| 6.3 | Large dataset (optional) | |
| 6.4 | Special characters (optional) | |

**Minimum passing criteria:** All Phase 1-5 tests pass.

## Troubleshooting

### `dolt push` fails with authentication error

- Verify SSH: `ssh -T git@github.com`
- For HTTPS: set `DOLT_REMOTE_PASSWORD` env var with a GitHub PAT
- Ensure the repo exists and you have write access

### `dolt push` fails with "object not found" or similar

- Ensure Dolt version is >= 1.81.8 (git remote support was added then)
- Try `dolt gc` before pushing
- Check `dolt status` for uncommitted changes

### `dolt clone` fails

- Verify the repo URL is correct
- Verify refs exist: `git ls-remote "$GITHUB_REPO" 'refs/dolt/*'`
- Try with `--ref` flag: `dolt clone --ref refs/dolt/data "$GITHUB_REPO" dest`

### Cleanup `git push :refs/dolt/data` fails

- Repo may have branch protection rules — disable them temporarily
- Try: `git push origin --delete refs/dolt/data`
