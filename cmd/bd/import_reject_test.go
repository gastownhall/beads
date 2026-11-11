package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
)

// GH#4492: a JSONL import aborted in its entirety on the first record the
// writer refused, and reported nothing useful about which record or why. These
// tests, adapted from @maphew's PR #5202, pin the replacement contract at the
// screen's seams: the abort stays the default, but it names the offending line
// and the way past it.

// Strict-by-default keeps the reporter of GH#4492 in exactly the position they
// started in: plain `bd import` on a file with one bad row still imports
// nothing. The only thing that changes their outcome is the failure telling
// them the escape hatch exists, so that hint is load-bearing, not decoration.
func TestFirstRejectErrorNamesTheEscapeHatch(t *testing.T) {
	err := strictImportError([]importRejection{
		{Line: 2, ID: "tst-2", Reason: "invalid status: verify"},
		{Line: 4, ID: "tst-4", Reason: "title is required"},
	})
	if err == nil {
		t.Fatal("strictImportError = nil, want an error")
	}
	msg := err.Error()
	for _, want := range []string{"line 2", "tst-2", "invalid status: verify", "and 1 more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	for _, want := range []string{"--skip-invalid", "--rejects"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not tell the user about %s; without it the default leaves\n"+
				"the reporter of GH#4492 with the same empty import they filed about:\n%s", want, msg)
		}
	}
	// Nothing partial landed, and the message has to say so: a user who thinks
	// some rows imported will fix the file and re-import, double-applying it.
	if !strings.Contains(msg, "nothing was imported") {
		t.Errorf("error does not say the import was a no-op:\n%s", msg)
	}
}

// --rejects is user-supplied and writeImportRejects renames a temp file into
// place, so a --rejects that names the import source through any alias must be
// refused rather than replacing the source with a quarantine.
func TestRejectPathCollidesWithSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "issues.jsonl")
	if err := os.WriteFile(source, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Direct same path.
	if !sameImportFile(source, source) {
		t.Fatal("sameImportFile = false for the identical path")
	}

	// Relative alias of the same file.
	rel := filepath.Join(dir, "..", filepath.Base(dir), "issues.jsonl")
	if !sameImportFile(rel, source) {
		t.Fatal("sameImportFile = false for a relative alias of the same file")
	}

	// A different file in the same directory does not collide, existing or
	// not yet created.
	other := filepath.Join(dir, "issues.rejected.jsonl")
	if sameImportFile(other, source) {
		t.Fatal("sameImportFile = true for a distinct file that does not exist yet")
	}
	if err := os.WriteFile(other, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if sameImportFile(other, source) {
		t.Fatal("sameImportFile = true for a distinct existing file")
	}

	// The flag check refuses the collision. A piped stdin is no file, so
	// nothing collides with it (TestRejectPathCollidesWithRedirectedStdin
	// covers a stdin that is a file).
	origSkip, origRejects := importSkipInvalid, importRejectsPath
	t.Cleanup(func() { importSkipInvalid, importRejectsPath = origSkip, origRejects })
	importSkipInvalid, importRejectsPath = true, rel
	if err := validateImportRejectsFlag(source, nil); err == nil || !strings.Contains(err.Error(), "is the input file") {
		t.Fatalf("validateImportRejectsFlag = %v, want it to refuse the input file", err)
	}
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pipeR.Close(); _ = pipeW.Close() })
	if err := validateImportRejectsFlag("", pipeR); err != nil {
		t.Fatalf("validateImportRejectsFlag(piped stdin) = %v, want nil", err)
	}

	// A hard-link alias has a distinct canonical path but the same underlying
	// file, so it must be refused too.
	hardLinkPath := filepath.Join(dir, "hard-link.jsonl")
	if err := os.Link(source, hardLinkPath); err != nil {
		t.Skipf("hard links unsupported in this environment: %v", err)
	}
	if !sameImportFile(hardLinkPath, source) {
		t.Fatal("sameImportFile = false for a hard link to the source")
	}

	// A symlinked reject path that resolves to the source path collides too.
	symlinkPath := filepath.Join(dir, "alias.jsonl")
	if err := os.Symlink(source, symlinkPath); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}
	if !sameImportFile(symlinkPath, source) {
		t.Fatal("sameImportFile = false for a symlink to the source")
	}
}

// `bd import - --skip-invalid --rejects F < F` reads F through stdin, where
// the path check above has no source path to compare, and writeImportRejects
// would rename the rejects (an empty file when every row is valid) over F
// before the import wrote anything. A stdin that is a regular file is the
// input as much as a named one: --rejects naming it through any alias is
// refused, and any other path, existing or not, is not.
func TestRejectPathCollidesWithRedirectedStdin(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "backup.jsonl")
	other := filepath.Join(dir, "other.jsonl")
	for _, path := range []string{source, other} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	stdin, err := os.Open(source)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close() })

	origSkip, origRejects := importSkipInvalid, importRejectsPath
	t.Cleanup(func() { importSkipInvalid, importRejectsPath = origSkip, origRejects })
	importSkipInvalid = true
	check := func(rejects string, wantRefused bool) {
		t.Helper()
		importRejectsPath = rejects
		err := validateImportRejectsFlag("", stdin)
		if refused := err != nil && strings.Contains(err.Error(), "is the input file"); refused != wantRefused {
			t.Errorf("validateImportRejectsFlag(--rejects %s, stdin from %s) = %v, want refused=%v", rejects, source, err, wantRefused)
		}
	}

	check(source, true)
	check(filepath.Join(dir, "..", filepath.Base(dir), "backup.jsonl"), true)
	check(other, false)
	check(filepath.Join(dir, "rejects.jsonl"), false) // does not exist yet

	hardLink := filepath.Join(dir, "hard-link.jsonl")
	if err := os.Link(source, hardLink); err != nil {
		t.Skipf("hard links unsupported in this environment: %v", err)
	}
	check(hardLink, true)
	symlink := filepath.Join(dir, "alias.jsonl")
	if err := os.Symlink(source, symlink); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}
	check(symlink, true)
}

// A dependency is saved on the line of the record that declares it, so when
// the screen rejects record B, the writer drops the edges kept records
// declare on B ("target not found"), and only re-importing the declaring
// lines brings them back. The import reports count them.
func TestDependenciesOnRejected(t *testing.T) {
	t.Run("from the writer's skipped dependencies", func(t *testing.T) {
		rejected := []importRejection{
			{Line: 2, ID: "tst-b", Reason: "invalid status: verify"},
			{Line: 7, Reason: "title is required"}, // a title-only row: nothing can depend on it
		}
		skipped := []string{
			"tst-a -> tst-b: target not found",
			"tst-c -> tst-b: target not found",
			"tst-c -> tst-x: target not found",   // a target the file never held
			"tst-d -> tst-b.1: target not found", // a child id, not the rejected record
		}
		if got := dependenciesOnRejected(skipped, rejected); got != 2 {
			t.Errorf("dependenciesOnRejected = %d, want 2 (tst-a and tst-c on tst-b)", got)
		}
		if got := dependenciesOnRejected(skipped, nil); got != 0 {
			t.Errorf("dependenciesOnRejected with nothing rejected = %d, want 0", got)
		}
	})

	t.Run("from the kept rows, for an importer that reports none", func(t *testing.T) {
		rejected := []importRejection{{Line: 2, ID: "tst-b"}, {Line: 9, ID: "oth-b"}, {Line: 11, ID: "tst-e"}}
		kept := []*types.Issue{
			{ID: "tst-a", Dependencies: []*types.Dependency{{DependsOnID: "tst-b", Type: types.DepBlocks}}},
			{ID: "tst-c", Dependencies: []*types.Dependency{
				{IssueID: "tst-c", DependsOnID: "tst-b", Type: types.DepParentChild},
				{DependsOnID: "oth-b", Type: types.DepBlocks}, // another prefix: external, never looked up
			}},
			// tst-e has a rejected row, but this kept duplicate writes it.
			{ID: "tst-d", Dependencies: []*types.Dependency{{DependsOnID: "tst-e", Type: types.DepBlocks}}},
			{ID: "tst-e"},
			// A retaining sweep retired tst-r: the import does not write it.
			{ID: "tst-r", Dependencies: []*types.Dependency{{DependsOnID: "tst-b", Type: types.DepBlocks}}},
		}
		if got := declaredDependenciesOnRejected(kept, rejected, []string{"tst-r"}); got != 2 {
			t.Errorf("declaredDependenciesOnRejected = %d, want 2 (tst-a and tst-c on tst-b)", got)
		}
	})
}

// The rejection reports name the dependencies lost with the skipped records,
// and the remedy that restores both: the records fixed in the source (or
// their status or type registered) and the whole source re-imported. The
// saved lines alone would not do it, since they do not carry the edges other
// records declare on them.
func TestRejectionReportsPointAtTheSource(t *testing.T) {
	rejected := []importRejection{{Line: 2, ID: "tst-b", Reason: "invalid status: verify", raw: []byte(`{"id":"tst-b"}`)}}
	skipped := []string{"tst-a -> tst-b: target not found", "tst-c -> tst-b: target not found"}

	t.Run("bd import", func(t *testing.T) {
		var out strings.Builder
		renderImportRejections(&out, importResultJSON{Rejected: rejected, RejectsFile: "r.jsonl", SkippedDependencies: skipped})
		for _, want := range []string{"line 2 (issue tst-b)", "Skipped lines saved to r.jsonl", "Also skipped 2 dependencies", "fix the skipped records in the original input", "re-import all of it"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("rejection report missing %q:\n%s", want, out.String())
			}
		}
		out.Reset()
		renderImportRejections(&out, importResultJSON{Rejected: rejected})
		if strings.Contains(out.String(), "dependencies") {
			t.Errorf("a run that skipped no dependency on the rejected records reports one:\n%s", out.String())
		}
	})

	t.Run("recovery", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "issues.jsonl")
		var out strings.Builder
		if got := quarantineRecoveryRejects(&out, source, rejected); got != source+".rejected.jsonl" {
			t.Fatalf("quarantineRecoveryRejects = %q, want the quarantine beside the source", got)
		}
		warnRecoveryDependenciesOnRejected(&out, source, 2)
		warnRecoveryDependenciesOnRejected(&out, source, 0)
		text := out.String()
		arg, note := remedy.CommandArg(runtime.GOOS, source, "<file>")
		for _, want := range []string{
			"saved to " + source + ".rejected.jsonl",
			"fix them in " + source,
			"re-import it: bd import " + arg + note + "\n",
			"also skipped 2 dependencies other records declare on the skipped records; re-importing " + source,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("recovery warning missing %q:\n%s", want, text)
			}
		}
		if strings.Count(text, "dependencies other records declare") != 1 {
			t.Errorf("want exactly one dependency warning (none for a count of 0):\n%s", text)
		}
	})
}

// TestWriteRejectFileHardening pins the write-path properties of the rejects
// file: a pre-existing symlink at the path is replaced as a link (never
// followed: a recovery's <source>.rejected.jsonl has no collision guard), a
// pre-existing file with a laxer mode is replaced with a fresh 0600 file
// rather than keeping its mode, and a run with nothing to save leaves an
// empty file at the path rather than removing it.
func TestWriteRejectFileHardening(t *testing.T) {
	rejects := []importRejection{{Line: 1, Reason: "r", raw: []byte(`{"id":"x"}`)}}

	t.Run("symlink at quarantine path is replaced, target untouched", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation is a privileged operation on default Windows configurations")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "precious.txt")
		const keep = "do not clobber\n"
		if err := os.WriteFile(target, []byte(keep), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "issues.jsonl.rejected.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		if err := writeImportRejects(link, rejects); err != nil {
			t.Fatalf("writeImportRejects: %v", err)
		}
		if data, rerr := os.ReadFile(target); rerr != nil || string(data) != keep {
			t.Errorf("symlink target changed: content=%q err=%v (quarantine write followed the link)", string(data), rerr)
		}
		fi, lerr := os.Lstat(link)
		if lerr != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("quarantine path is still a symlink (or missing): err=%v", lerr)
		}
		if data, _ := os.ReadFile(link); string(data) != "{\"id\":\"x\"}\n" {
			t.Errorf("quarantine = %q, want the rejected line", data)
		}
	})

	t.Run("pre-existing lax mode is replaced with 0600", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("requires POSIX file permission semantics")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "rejects.jsonl")
		if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeImportRejects(path, rejects); err != nil {
			t.Fatalf("writeImportRejects: %v", err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("quarantine mode = %o, want 0600 (rewrite must not inherit the old lax mode)", fi.Mode().Perm())
		}
	})

	t.Run("nothing to write leaves an empty file, not no file", func(t *testing.T) {
		dir := t.TempDir()
		// A file the user named that holds an earlier run's rejects.
		path := filepath.Join(dir, "notes.jsonl")
		if err := os.WriteFile(path, []byte("an earlier run's rejects\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeImportRejects(path, nil); err != nil {
			t.Fatalf("writeImportRejects: %v", err)
		}
		// The path still exists, so a later import can still write to it, and
		// it no longer holds rejects this run did not produce.
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
			t.Fatalf("path not a regular empty file: stat err=%v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("directory holds %d entries, want only the rejects file (no temp file left behind)", len(entries))
		}
	})
}

// TestResolveImportRejectsDryRunLeavesRejectsFileAlone pins the --dry-run
// filesystem contract (adapted from @maphew's PR #5202): a dry run reports
// what it would set aside but never creates, rewrites or removes the
// --rejects file, whether or not it found anything to reject, and a strict
// refusal stops before the filesystem is touched at all.
func TestResolveImportRejectsDryRunLeavesRejectsFileAlone(t *testing.T) {
	origSkip, origRejects := importSkipInvalid, importRejectsPath
	t.Cleanup(func() { importSkipInvalid, importRejectsPath = origSkip, origRejects })

	ctx := context.Background()
	noCustom := func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	bad := &types.Issue{ID: "tst-x", Title: "Bad", Status: types.Status("verify"), IssueType: types.TypeTask}
	sources := map[*types.Issue]importSource{bad: {line: 3, raw: []byte(`{"id":"tst-x"}`)}}
	rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
	importRejectsPath = rejectsPath

	t.Run("dry run with rejects creates no file", func(t *testing.T) {
		importSkipInvalid = true
		_, rejected, rejectsFile, err := screenImportWriteSet(ctx, []*types.Issue{bad}, sources, noCustom, true)
		if err != nil {
			t.Fatalf("screenImportWriteSet: %v", err)
		}
		if len(rejected) != 1 || rejected[0].Line != 3 {
			t.Errorf("rejected = %+v, want the line-3 row", rejected)
		}
		if rejectsFile != "" {
			t.Errorf("rejectsFile = %q, want empty on a dry run", rejectsFile)
		}
		if _, statErr := os.Stat(rejectsPath); !os.IsNotExist(statErr) {
			t.Errorf("dry run wrote the rejects file (stat err: %v)", statErr)
		}
	})

	const keep = "previous run's quarantine content\n"
	if err := os.WriteFile(rejectsPath, []byte(keep), 0o600); err != nil {
		t.Fatalf("seed rejects file: %v", err)
	}
	assertKept := func(t *testing.T) {
		t.Helper()
		data, err := os.ReadFile(rejectsPath)
		if err != nil {
			t.Fatalf("pre-existing rejects file gone after dry run: %v", err)
		}
		if string(data) != keep {
			t.Errorf("pre-existing rejects file content changed: %q", data)
		}
	}

	t.Run("dry run without rejects preserves a pre-existing file", func(t *testing.T) {
		importSkipInvalid = true
		if _, _, _, err := screenImportWriteSet(ctx, nil, nil, noCustom, true); err != nil {
			t.Fatalf("screenImportWriteSet: %v", err)
		}
		assertKept(t)
	})

	t.Run("dry run with rejects preserves a pre-existing file", func(t *testing.T) {
		importSkipInvalid = true
		if _, _, _, err := screenImportWriteSet(ctx, []*types.Issue{bad}, sources, noCustom, true); err != nil {
			t.Fatalf("screenImportWriteSet: %v", err)
		}
		assertKept(t)
	})

	t.Run("strict refusal leaves the file alone", func(t *testing.T) {
		importSkipInvalid = false
		for _, dryRun := range []bool{true, false} {
			if _, _, _, err := screenImportWriteSet(ctx, []*types.Issue{bad}, sources, noCustom, dryRun); err == nil || !strings.Contains(err.Error(), "line 3") {
				t.Fatalf("dryRun=%v: err = %v, want the strict refusal naming line 3", dryRun, err)
			}
			assertKept(t)
		}
	})
}
