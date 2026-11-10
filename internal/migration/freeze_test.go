package migration

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// pinNoOverride neutralizes an inherited BD_MIGRATION_FREEZE_FILE without
// making it authoritative. Find/FindFrom short-circuit on a set variable, so a
// developer who exported the documented opt-out would otherwise see every
// walk-based test in this file fail on a healthy checkout.
func pinNoOverride(t *testing.T) {
	t.Helper()
	t.Setenv(EnvFreezeFile, "")
}

// writeMarker writes a freeze marker at dir/MIGRATION-FREEZE and returns its path.
func writeMarker(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing marker at %s: %v", path, err)
	}
	return path
}

// captureStderr runs f with os.Stderr redirected to a pipe and returns what it
// wrote. These tests run sequentially (no t.Parallel), so swapping the
// process-global stderr for the duration of one call is safe here.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	f()

	if err := w.Close(); err != nil {
		t.Fatalf("closing stderr pipe: %v", err)
	}
	os.Stderr = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stderr: %v", err)
	}
	return string(out)
}

// TestFindFromWalkTerminates covers the walk's not-found exit. FindFrom takes
// the start directory explicitly, so this runs without touching process state
// and without the "a real marker might exist above TMPDIR" caveat that forced
// the old no-marker test to pin the override and skip the walk entirely.
// Breaking the `parent == dir` termination — hanging at /, or returning the
// root candidate — fails here instead of passing the whole suite.
func TestFindFromWalkTerminates(t *testing.T) {
	pinNoOverride(t)

	// An absolute path that does not exist: every ancestor up to / is walked,
	// nothing is found, and the loop must stop at the root.
	deep := filepath.Join(t.TempDir(), "a", "b", "c", "d")
	res := FindFrom(deep)
	if res.Err != nil {
		t.Fatalf("FindFrom(%q).Err = %v, want nil", deep, res.Err)
	}
	if res.Path != "" {
		t.Fatalf("FindFrom(%q).Path = %q, want \"\" — no marker exists anywhere above a fresh temp dir", deep, res.Path)
	}
	if res.Frozen() {
		t.Errorf("Frozen() = true for an empty result")
	}
}

func TestFindFromMarkerInStartDir(t *testing.T) {
	pinNoOverride(t)
	dir := t.TempDir()
	want := writeMarker(t, dir, "operator\t2026-08-16T12:00:00Z\tmigrating\n")

	res := FindFrom(dir)
	if !res.Frozen() {
		t.Fatalf("FindFrom(%q) = %+v, want the marker in the start directory", dir, res)
	}
	if !sameFile(t, res.Path, want) {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
	if res.FromEnv {
		t.Errorf("FromEnv = true for a walk-found marker")
	}
}

func TestFindFromWalksUpToGrandparent(t *testing.T) {
	pinNoOverride(t)
	root := t.TempDir()
	want := writeMarker(t, root, "")
	deep := filepath.Join(root, "rig", "repo")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatalf("creating %s: %v", deep, err)
	}

	res := FindFrom(deep)
	if !res.Frozen() {
		t.Fatalf("FindFrom(%q) = %+v, want the marker two levels up at %q", deep, res, want)
	}
	if !sameFile(t, res.Path, want) {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
}

// TestFindWalksExtraRootsNotJustCwd is the target-workspace contract: a caller
// whose working directory is nowhere near the frozen tree must still be
// stopped when it names that tree. This is the `BEADS_DIR=... bd create` and
// `bd -C <dir>` shape, neither of which chdirs.
func TestFindWalksExtraRootsNotJustCwd(t *testing.T) {
	pinNoOverride(t)
	unrelated := t.TempDir()
	frozenRoot := t.TempDir()
	want := writeMarker(t, frozenRoot, "operator\t\treason\n")
	workspace := filepath.Join(frozenRoot, "repo", ".beads")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("creating %s: %v", workspace, err)
	}
	t.Chdir(unrelated)

	if res := Find(); res.Frozen() {
		t.Fatalf("Find() with an unrelated cwd = %+v, want not frozen (guards the test's own premise)", res)
	}

	res := Find(workspace)
	if !res.Frozen() {
		t.Fatalf("Find(%q) = %+v, want the marker above the named workspace", workspace, res)
	}
	if !sameFile(t, res.Path, want) {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
}

// TestFindEnvOverrideAuthoritative pins both directions of the override: an
// existing file freezes even with no ancestor marker, and a missing file does
// NOT freeze even when an ancestor marker exists. The second half is what
// makes the variable a usable opt-out.
func TestFindEnvOverrideAuthoritative(t *testing.T) {
	t.Run("existing file freezes without any ancestor marker", func(t *testing.T) {
		work := t.TempDir()
		override := filepath.Join(t.TempDir(), "freeze-marker")
		if err := os.WriteFile(override, []byte("operator\t\treason\n"), 0644); err != nil {
			t.Fatalf("writing override marker: %v", err)
		}
		t.Chdir(work)
		t.Setenv(EnvFreezeFile, override)

		res := Find()
		if res.Path != override {
			t.Errorf("Path = %q, want the override path %q", res.Path, override)
		}
		if !res.FromEnv {
			t.Errorf("FromEnv = false, want true — the refusal needs it to offer the unset hint")
		}
	})

	t.Run("missing file wins over an ancestor marker", func(t *testing.T) {
		root := t.TempDir()
		writeMarker(t, root, "operator\t\treason\n")
		deep := filepath.Join(root, "nested")
		if err := os.MkdirAll(deep, 0755); err != nil {
			t.Fatalf("creating %s: %v", deep, err)
		}
		t.Chdir(deep)
		t.Setenv(EnvFreezeFile, filepath.Join(root, "no-such-marker"))

		res := Find()
		if res.Frozen() {
			t.Errorf("Find() = %+v, want not frozen — the override must skip the ancestor walk entirely", res)
		}
	})

	t.Run("override also outranks an explicitly named workspace", func(t *testing.T) {
		root := t.TempDir()
		writeMarker(t, root, "")
		t.Chdir(t.TempDir())
		t.Setenv(EnvFreezeFile, filepath.Join(root, "no-such-marker"))

		if res := Find(root); res.Frozen() {
			t.Errorf("Find(%q) = %+v, want not frozen", root, res)
		}
	})
}

// TestLookupFailureIsFrozen is the fail-closed contract: a marker path bd
// cannot resolve is not the same as a marker that is absent, and the ambiguity
// must not silently disarm the gate.
func TestLookupFailureIsFrozen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not produce the same stat failure on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny the stat")
	}

	t.Run("env override in an unreadable directory", func(t *testing.T) {
		locked := filepath.Join(t.TempDir(), "locked")
		if err := os.Mkdir(locked, 0755); err != nil {
			t.Fatalf("creating %s: %v", locked, err)
		}
		override := filepath.Join(locked, FileName)
		if err := os.WriteFile(override, nil, 0644); err != nil {
			t.Fatalf("writing override marker: %v", err)
		}
		if err := os.Chmod(locked, 0000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0755) })
		t.Setenv(EnvFreezeFile, override)

		res := Find()
		if res.Err == nil {
			t.Fatalf("Find() = %+v, want a lookup error for an unstattable authoritative path", res)
		}
		if !res.Frozen() {
			t.Errorf("Frozen() = false on a lookup failure — an undeterminable gate must not read as an open one")
		}
		if !res.FromEnv {
			t.Errorf("FromEnv = false, want true so the refusal can name the variable")
		}
	})

	t.Run("unreadable ancestor during the walk", func(t *testing.T) {
		locked := filepath.Join(t.TempDir(), "locked")
		nested := filepath.Join(locked, "repo")
		if err := os.MkdirAll(nested, 0755); err != nil {
			t.Fatalf("creating %s: %v", nested, err)
		}
		if err := os.Chmod(locked, 0000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0755) })
		pinNoOverride(t)

		res := FindFrom(nested)
		if res.Err == nil {
			t.Fatalf("FindFrom(%q) = %+v, want a lookup error for an unreadable ancestor", nested, res)
		}
		if !res.Frozen() {
			t.Errorf("Frozen() = false on a lookup failure")
		}
	})
}

// TestNonRegularMarkerIsNotAFreeze covers the vendored-fixture / extracted-
// archive case: a *directory* named MIGRATION-FREEZE must not freeze every
// workspace below it with an unreadable, unremovable-sounding refusal.
func TestNonRegularMarkerIsNotAFreeze(t *testing.T) {
	pinNoOverride(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, FileName), 0755); err != nil {
		t.Fatalf("creating marker directory: %v", err)
	}
	nested := filepath.Join(root, "repo")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("creating %s: %v", nested, err)
	}

	if res := FindFrom(nested); res.Frozen() {
		t.Errorf("FindFrom(%q) = %+v, want not frozen — a directory is not a marker", nested, res)
	}
}

// TestEnvOverrideFollowsSymlink is the fail-open regression on the authoritative
// path: an operator who points BD_MIGRATION_FREEZE_FILE through an indirection
// symlink (the `current -> releases/N` pattern the variable exists for) must
// still be frozen when the link resolves to a real marker. os.Lstat alone would
// see the link, not the regular file it targets, and wave the write through.
func TestEnvOverrideFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real-marker")
	if err := os.WriteFile(target, []byte("alice\t\tmigrating\n"), 0644); err != nil {
		t.Fatalf("writing target marker: %v", err)
	}
	link := filepath.Join(dir, "current-freeze")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	t.Chdir(t.TempDir())
	t.Setenv(EnvFreezeFile, link)

	res := Find()
	if !res.Frozen() {
		t.Fatalf("Find() with %s at a symlink to a real marker = %+v, want frozen — the authoritative path must follow the link", EnvFreezeFile, res)
	}
	if res.Path != link {
		t.Errorf("Path = %q, want the operator-named path %q", res.Path, link)
	}
	if !res.FromEnv {
		t.Errorf("FromEnv = false, want true")
	}
}

// TestEnvOverrideDanglingSymlinkNotFrozen keeps the opt-out honest: a symlink
// whose target does not exist is the same as naming a missing path — no marker,
// no freeze — not an undeterminable failure that would fail closed.
func TestEnvOverrideDanglingSymlinkNotFrozen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "current-freeze")
	if err := os.Symlink(filepath.Join(dir, "no-such-target"), link); err != nil {
		t.Fatalf("creating dangling symlink: %v", err)
	}
	t.Chdir(t.TempDir())
	t.Setenv(EnvFreezeFile, link)

	res := Find()
	if res.Frozen() {
		t.Errorf("Find() with %s at a dangling symlink = %+v, want not frozen", EnvFreezeFile, res)
	}
}

// TestWalkSymlinkMarkerIgnoredWithWarning is the untrusted-walk half of the
// symlink rule: a symlink named MIGRATION-FREEZE that the ancestor walk hits is
// NOT followed — otherwise anything the walk merely passed through could
// redirect the gate — but the skip must be announced, not a silent fail-open.
func TestWalkSymlinkMarkerIgnoredWithWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows")
	}
	pinNoOverride(t)
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "real-marker")
	if err := os.WriteFile(target, nil, 0644); err != nil {
		t.Fatalf("writing target marker: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, FileName)); err != nil {
		t.Fatalf("creating symlink marker: %v", err)
	}
	nested := filepath.Join(root, "repo")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("creating %s: %v", nested, err)
	}

	var res Result
	stderr := captureStderr(t, func() { res = FindFrom(nested) })
	if res.Frozen() {
		t.Errorf("FindFrom(%q) = %+v, want not frozen — a symlinked marker on the untrusted walk is not followed", nested, res)
	}
	if !strings.Contains(stderr, FileName) || !strings.Contains(stderr, "symlink") {
		t.Errorf("stderr = %q, want a warning naming the ignored symlinked %s so the skip is not silent", stderr, FileName)
	}
}

// TestStickyWorldWritableMarkerIgnored covers the shared-machine denial of
// service: /tmp is world-writable and sticky, so any local account can drop a
// marker there that freezes every workspace below it AND that the victim
// cannot delete. Markers found by the walk in such a directory are ignored.
func TestStickyWorldWritableMarkerIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sticky bit on Windows")
	}
	pinNoOverride(t)
	root := t.TempDir()
	writeMarker(t, root, "attacker\t\tdenial of service\n")
	if err := os.Chmod(root, os.ModeSticky|0777); err != nil {
		t.Fatalf("chmod sticky+world-writable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0755) })
	nested := filepath.Join(root, "repo")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("creating %s: %v", nested, err)
	}

	if res := FindFrom(nested); res.Frozen() {
		t.Errorf("FindFrom(%q) = %+v, want not frozen — a marker in a sticky world-writable directory is forgeable and undeletable", nested, res)
	}

	// The operator's own explicit path is exempt: naming it is intent.
	override := filepath.Join(root, FileName)
	t.Setenv(EnvFreezeFile, override)
	if res := Find(); !res.Frozen() {
		t.Errorf("Find() with %s=%q = %+v, want frozen — an explicitly named marker is not second-guessed", EnvFreezeFile, override, res)
	}
}

func TestReadFileParsesOperatorTimestampReason(t *testing.T) {
	dir := t.TempDir()
	ts := "2026-08-16T12:00:00Z"
	path := writeMarker(t, dir, "operator\t"+ts+"\tdolt v2 migration\n")

	info := ReadFile(path)
	if info == nil {
		t.Fatalf("ReadFile(%q) = nil, want parsed Info", path)
	}
	if info.Operator != "operator" {
		t.Errorf("Operator = %q, want %q", info.Operator, "operator")
	}
	if info.Reason != "dolt v2 migration" {
		t.Errorf("Reason = %q, want %q", info.Reason, "dolt v2 migration")
	}
	wantTS, _ := time.Parse(time.RFC3339, ts)
	if !info.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", info.Timestamp, wantTS)
	}
}

// TestReadFileLeadingEmptyOperatorKeepsFields is the field-shift regression: a
// marker that records a timestamp and reason but no operator must keep them in
// their own fields, not slide the timestamp into Operator and drop Reason.
func TestReadFileLeadingEmptyOperatorKeepsFields(t *testing.T) {
	dir := t.TempDir()
	ts := "2026-08-16T12:00:00Z"
	path := writeMarker(t, dir, "\t"+ts+"\tdisk swap\n")

	info := ReadFile(path)
	if info == nil {
		t.Fatalf("ReadFile(%q) = nil, want parsed Info", path)
	}
	if info.Operator != "" {
		t.Errorf("Operator = %q, want \"\" — the payload records no operator", info.Operator)
	}
	if info.Reason != "disk swap" {
		t.Errorf("Reason = %q, want %q — a leading empty field must not shift the rest", info.Reason, "disk swap")
	}
	wantTS, _ := time.Parse(time.RFC3339, ts)
	if !info.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", info.Timestamp, wantTS)
	}
}

// TestReadFileTakesFirstLineOnly keeps a multi-line marker from smuggling
// extra lines into Reason, which is echoed to a terminal.
func TestReadFileTakesFirstLineOnly(t *testing.T) {
	dir := t.TempDir()
	path := writeMarker(t, dir, "operator\t\treal reason\nError: spoofed second line\n")

	info := ReadFile(path)
	if info == nil {
		t.Fatalf("ReadFile(%q) = nil, want parsed Info", path)
	}
	if info.Reason != "real reason" {
		t.Errorf("Reason = %q, want %q (no trailing lines)", info.Reason, "real reason")
	}
	if strings.Contains(info.Reason, "spoofed") || strings.Contains(info.Operator, "spoofed") {
		t.Errorf("parsed fields carry a later line: %+v", info)
	}
}

func TestReadFileCRLF(t *testing.T) {
	dir := t.TempDir()
	path := writeMarker(t, dir, "operator\t\twindows reason\r\n")

	info := ReadFile(path)
	if info == nil {
		t.Fatalf("ReadFile(%q) = nil", path)
	}
	if info.Reason != "windows reason" {
		t.Errorf("Reason = %q, want %q (CR stripped)", info.Reason, "windows reason")
	}
}

// TestReadFileCapsSize keeps a hostile or accidental giant marker from turning
// the refusal into a full-size allocation.
func TestReadFileCapsSize(t *testing.T) {
	dir := t.TempDir()
	huge := strings.Repeat("x", maxMarkerSize*2)
	path := writeMarker(t, dir, "operator\t\t"+huge+"\n")

	info := ReadFile(path)
	if info == nil {
		t.Fatalf("ReadFile(%q) = nil", path)
	}
	if len(info.Reason) > maxMarkerSize {
		t.Errorf("len(Reason) = %d, want <= %d — the read must be capped", len(info.Reason), maxMarkerSize)
	}
}

func TestReadFileMissingReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if info := ReadFile(filepath.Join(dir, FileName)); info != nil {
		t.Fatalf("ReadFile = %+v, want nil (no marker present)", info)
	}
	if info := ReadFile(""); info != nil {
		t.Fatalf("ReadFile(\"\") = %+v, want nil", info)
	}
}

func TestReadFileEmptyContentDoesNotFabricateTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := writeMarker(t, dir, "")

	info := ReadFile(path)
	if info == nil {
		t.Fatalf("ReadFile(%q) = nil for an empty-but-present marker, want non-nil Info", path)
	}
	if !info.Timestamp.IsZero() {
		t.Errorf("Timestamp = %v, want zero value (no recorded timestamp to report — must not fabricate one)", info.Timestamp)
	}
	if info.Operator != "" || info.Reason != "" {
		t.Errorf("Operator/Reason = %q/%q, want both empty for an empty marker", info.Operator, info.Reason)
	}
}

func TestReadFileMalformedContentDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	path := writeMarker(t, dir, "not tab separated at all")

	info := ReadFile(path)
	if info == nil {
		t.Fatalf("ReadFile(%q) = nil for a malformed-but-present marker, want non-nil Info with best-effort fields", path)
	}
	if info.Operator != "not tab separated at all" {
		t.Errorf("Operator = %q, want the whole line back (no tabs to split on)", info.Operator)
	}
}

// sameFile compares two paths by identity rather than string equality: on
// macOS t.TempDir() lives under /var, a symlink to /private/var, so the walk's
// filepath.Abs spelling can differ from the one the test holds.
func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(ai, bi)
}
