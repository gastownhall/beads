# Analysis: Getting Started Documentation vs. Reality

**Date:** 2026-04-05
**Requested by:** @csells
**Question:** "How much of the getting started docs are genuinely necessary and how many point to errors that should be fixed?"

## Executive Summary

The getting started documentation is **~3x longer than it needs to be**, largely
because it documents workarounds for problems that should be fixed in the tool
itself. Of the ~1,600 lines across the core getting-started docs (README quick
start, QUICKSTART.md, INSTALLING.md, SETUP.md), roughly:

| Category | Est. Lines | % of Total | Verdict |
|----------|-----------|------------|---------|
| **Genuinely necessary** (core concepts, happy path) | ~400 | 25% | Keep |
| **Useful reference** (platform variants, IDE matrix) | ~350 | 22% | Keep but consolidate |
| **Workarounds for fixable UX issues** | ~450 | 28% | Fix the tool, delete the docs |
| **Redundant/duplicated across files** | ~400 | 25% | Consolidate into one place |

**Bottom line:** About half the getting-started surface area compensates for
tool-level UX gaps. If those gaps were fixed, the docs could shrink by ~50% and
the remaining docs would be more effective because users wouldn't have to read
past noise to find the signal.

---

## Part 1: What's Genuinely Necessary

These sections earn their keep. A new user *needs* this information and the tool
can't reasonably convey it automatically.

### 1.1 Core concept: "What is this and why would I use it?"
- **README.md** quick pitch (lines 1-30): Necessary. Explains the value prop.
- **QUICKSTART.md** "Why Beads?" section (lines 1-30): Good concrete example
  showing flat tracker vs. dependency-aware ready queue.

### 1.2 The actual happy path (5 commands)
```bash
brew install beads      # or npm install -g @beads/bd
cd your-project
bd init
bd create "Task" -p 1
bd ready
```
This is the irreducible core. Everything else is either reference or
workaround.

### 1.3 Dependency concepts
- `bd dep add`, `bd dep tree`, `bd ready --explain` — these are the
  differentiating feature. The QUICKSTART walkthrough (lines 98-230) is
  well-written and necessary.

### 1.4 Team sync basics
- "Add a Dolt remote, push, pull" — ~20 lines. Necessary for multi-machine use.

### 1.5 IDE setup matrix
- The table in INSTALLING.md (lines 22-31) mapping environments to components
  is genuinely useful. Users need to know "I use Cursor, what do I install?"

---

## Part 2: Workarounds That Point to Fixable Errors

These documentation sections exist because the tool has a UX gap. Each one
represents a place where the *tool should be smarter* so the *docs can be
shorter*.

### 2.1 `bd: command not found` / PATH issues (~40 lines across 3 files)

**Appears in:** INSTALLING.md, TROUBLESHOOTING.md, QUICKSTART.md

**The fix:** The install script already tries to handle PATH. But `bd` should
print a one-liner after install: "Add this to your shell profile: `export
PATH=...`" — or better, the Homebrew formula and npm package should handle
this automatically (Homebrew already does). The `go install` path is the
problem child.

**Recommendation:** Remove `go install` from the "Quick Install" section
entirely. It's a developer/contributor path, not a user path. Move it to
CONTRIBUTING.md. The happy path should be `brew install beads` or `npm install
-g @beads/bd` — both handle PATH automatically.

### 2.2 CGO / ICU / build dependency issues (~80 lines)

**Appears in:** INSTALLING.md (lines 107-130), README.md (lines 75-91),
CONTRIBUTING.md

**Root cause:** Users who `go install` need CGO + ICU headers. But prebuilt
binaries don't need any of this.

**The fix:** Same as above — stop promoting `go install` as a primary install
method. The prebuilt binary path (Homebrew, npm, install script) requires zero
build dependencies. CGO/ICU docs belong in CONTRIBUTING.md only.

### 2.3 `zsh: killed bd` / macOS crashes (~20 lines, duplicated in 2 files)

**Appears in:** INSTALLING.md, TROUBLESHOOTING.md (identical content)

**The fix:** This only affects `go install` without CGO. If `go install` is
demoted to a contributor path, this goes away for users. For contributors,
`make install` already handles it.

### 2.4 Windows Controlled Folder Access / Firewall (~40 lines)

**Appears in:** TROUBLESHOOTING.md

**The fix:** `bd init` should detect when it fails to create `.beads/` and
print a helpful message: "Windows Controlled Folder Access may be blocking
bd.exe. Add bd.exe to the whitelist in Windows Security settings." Currently
it hangs indefinitely, which is the real bug.

### 2.5 Multiple `bd` binaries in PATH (~30 lines)

**Appears in:** TROUBLESHOOTING.md

**The fix:** `bd version` (or `bd doctor`) should check for multiple `bd`
binaries in PATH and warn. Many CLI tools do this (e.g., `brew doctor`).

### 2.6 Port conflicts with multiple projects (~30 lines)

**Appears in:** TROUBLESHOOTING.md

**The fix:** Embedded mode (the default) doesn't have port conflicts. This
only applies to server mode. The docs should note this is server-mode-only
and `bd init` in server mode should detect port conflicts and suggest shared
server mode.

### 2.7 "Database is locked" (~20 lines)

**Appears in:** TROUBLESHOOTING.md

**The fix:** Embedded mode uses file locking. `bd` should detect the lock,
identify the holding PID, and print: "Another bd process (PID 12345) is using
the database. Wait for it to finish or kill it." Instead of a raw SQLite error.

### 2.8 Circuit breaker / stale state file (~50 lines)

**Appears in:** TROUBLESHOOTING.md

**Root cause:** A `/tmp/beads-dolt-circuit-*.json` file persists and blocks
all operations. Users have to know to manually delete it.

**The fix:** `bd doctor --fix` should clear stale circuit breaker files. Or
better: the circuit breaker should auto-reset after a configurable cooldown
(it has a 30s cooldown, but the state file persists across reboots on macOS
because `/tmp` -> `/private/tmp` isn't cleared).

### 2.9 `bd doctor` not working in embedded mode (~15 lines)

**Appears in:** TROUBLESHOOTING.md (indirectly), doctor.go code

**The fix:** This is the default mode! `bd doctor` should work in the default
mode. Telling users to switch to server mode to use the diagnostic tool is
backwards.

### 2.10 Sandbox mode documentation (~80 lines)

**Appears in:** TROUBLESHOOTING.md

**The fix:** Sandbox mode is auto-detected since v0.21.1. The docs still
describe manual `--sandbox` flag usage extensively. The auto-detection section
should be the primary content; the manual flags should be a small footnote.

### 2.11 Git hooks timeout / permission issues (~40 lines)

**Appears in:** TROUBLESHOOTING.md

**The fix:** `bd hooks install` should set correct permissions automatically
(it may already, but users still hit this). The timeout issue should be
documented in `bd hooks install` output, not buried in troubleshooting.

### 2.12 Antivirus false positives (~40 lines + separate ANTIVIRUS.md)

**Appears in:** TROUBLESHOOTING.md, docs/ANTIVIRUS.md

**The fix:** Code signing. The docs acknowledge this ("Future plans for code
signing"). Until then, the install script should print a note on Windows.
The extensive troubleshooting section is a band-aid.

---

## Part 3: Redundancy and Structural Issues

### 3.1 Installation instructions appear in 4 places

| Location | Content |
|----------|---------|
| README.md lines 65-99 | Homebrew, Go, npm, build from source |
| INSTALLING.md (full file) | Same + platform-specific + troubleshooting |
| QUICKSTART.md lines 33-38 | `go build` from source |
| CONTRIBUTING.md lines 16-31 | Clone + build from source |

**Recommendation:** README.md should have a 3-line install section linking to
INSTALLING.md. QUICKSTART.md should say "Install bd (see INSTALLING.md)" and
skip the build commands. CONTRIBUTING.md should cover dev setup only.

### 3.2 QUICKSTART.md uses `./bd` (local binary) syntax

The quickstart shows `./bd create`, `./bd ready`, etc. — suggesting the user
built from source and is running a local binary. This is the contributor
experience, not the user experience. Users who installed via Homebrew/npm
would just run `bd`.

**The fix:** Change all `./bd` references to `bd` in QUICKSTART.md.

### 3.3 The role/contributor/maintainer explanation is over-documented

Lines 69-96 in QUICKSTART.md + lines 60-63 in README.md + FAQ entries.
The `bd init` wizard already asks and explains this interactively.

**Recommendation:** One paragraph in QUICKSTART.md, link to a reference doc
for details.

### 3.4 SETUP.md is enormous (555 lines) for a `bd setup <tool>` reference

Most of SETUP.md documents `bd setup --check`, `bd setup --remove`, flags for
each of 10+ tools, comparison tables, custom recipes, etc.

**Recommendation:** This is reference documentation, not getting-started
documentation. Move it out of the getting-started path. Most users need
exactly: `bd setup claude` or `bd setup cursor`. One line each.

### 3.5 Database maintenance in QUICKSTART.md

Lines 295-340 cover compaction, cleanup, and migration — in a "quickstart"
doc. A new user creating their first issue doesn't need to know about
database garbage collection.

**Recommendation:** Move to ADVANCED.md or a dedicated MAINTENANCE.md.

### 3.6 Notion sync in QUICKSTART.md

Lines 257-290 cover Notion integration in the quickstart. This is an advanced
integration, not a getting-started topic.

---

## Part 4: Specific Recommendations

### Immediate wins (tool changes that eliminate doc sections)

1. **Demote `go install` to contributor docs** — eliminates CGO/ICU/PATH docs
   for users (~120 lines saved across files)
2. **Fix `bd init` hang on Windows Controlled Folder Access** — detect the
   failure and print a message instead of hanging
3. **Make `bd doctor` work in embedded mode** — this is the default mode
4. **Auto-clear stale circuit breaker files** — or make `bd doctor --fix`
   handle them
5. **Detect multiple `bd` binaries in PATH** — warn in `bd version` output
6. **Print better error for locked database** — show PID of holder

### Immediate wins (doc changes only)

1. **Change `./bd` to `bd` in QUICKSTART.md** — reflects actual user experience
2. **Remove database maintenance from QUICKSTART.md** — move to ADVANCED.md
3. **Remove Notion sync from QUICKSTART.md** — move to integrations doc
4. **Consolidate install instructions** — one canonical location (INSTALLING.md),
   links from everywhere else
5. **Trim SETUP.md from quickstart path** — it's reference, not onboarding
6. **Reduce role/contributor docs in quickstart** — trust the interactive wizard

### Dream state: The ideal getting-started experience

```bash
brew install beads    # or: npm install -g @beads/bd
cd my-project
bd init               # creates database, detects role, installs hooks
bd create "My first task" -p 1
bd ready              # shows the task
```

Five commands. No CGO. No PATH fiddling. No Dolt installation. No role
configuration docs. No circuit breaker files to delete. The tool handles it.

The docs for this should be ~50 lines in README.md plus a link to the
QUICKSTART walkthrough (~150 lines covering dependencies and the ready queue).
Everything else is reference documentation, not getting-started documentation.

---

## Appendix: Files Reviewed

| File | Lines | Role in Getting Started |
|------|-------|------------------------|
| README.md | 190 | Entry point, quick start, feature overview |
| docs/QUICKSTART.md | 355 | Tutorial walkthrough |
| docs/INSTALLING.md | 535 | Installation for all platforms |
| docs/SETUP.md | 555 | IDE/editor integration setup |
| docs/TROUBLESHOOTING.md | 1030 | Error recovery |
| docs/FAQ.md | 513 | Common questions |
| CONTRIBUTING.md | 367 | Developer setup |
| docs/SYNC_SETUP.md | ~100 | Multi-machine sync |
| AGENT_INSTRUCTIONS.md | ~100 | Agent dev workflow |

Also reviewed: `cmd/bd/init.go` (actual init flow), `cmd/bd/prime.go` (context
injection), `cmd/bd/doctor.go` (health checks), `scripts/install.sh` (installer).
