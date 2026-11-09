package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestImportRejectsFileMergesAndRefusesUnsafeDestinations pins the rejects
// file both `--skip-invalid --rejects` and the recovery quarantine write
// (GH#4492, AC-6): earlier rejected records survive a later save, an exact
// repeat is not duplicated, and a destination that would lose data — a
// symlink, a directory, the source itself or a hardlink to it — is refused
// before anything is imported.
func TestImportRejectsFileMergesAndRefusesUnsafeDestinations(t *testing.T) {
	r1 := importRejection{Line: 3, ID: "rq-1", Raw: []byte(`{"id":"rq-1","status":"verify","x_unknown":1}`)}
	r2 := importRejection{Line: 4, ID: "rq-2", Raw: []byte(`{"id":"rq-2","status":"verify"}`)}

	// save runs the explicit --skip-invalid --rejects path.
	save := func(source, rejects string, rejected ...importRejection) error {
		var result importResultJSON
		return applyImportRejections(&result, source, rejected, true, rejects)
	}
	newSource := func(t *testing.T, dir string) string {
		t.Helper()
		source := filepath.Join(dir, "issues.jsonl")
		if err := os.WriteFile(source, []byte("source data\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return source
	}
	assertFile := func(t *testing.T, path, want string) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Errorf("%s =\n%q\nwant\n%q", filepath.Base(path), got, want)
		}
	}

	t.Run("merges_with_earlier_records_and_dedupes_exact_lines", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		// An earlier quarantine whose last line has no newline.
		if err := os.WriteFile(rejects, []byte("old-1\nold-2"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := save(source, rejects, r1); err != nil {
			t.Fatalf("first save: %v", err)
		}
		if err := save(source, rejects, r1, r2); err != nil {
			t.Fatalf("second save: %v", err)
		}
		assertFile(t, rejects, "old-1\nold-2\n"+string(r1.Raw)+"\n"+string(r2.Raw)+"\n")
	})

	refused := func(t *testing.T, source, rejects string) {
		t.Helper()
		if err := save(source, rejects, r1); err == nil {
			t.Fatalf("saving the rejects to %s succeeded, want a refusal", rejects)
		}
		assertFile(t, source, "source data\n")
	}

	t.Run("refuses_a_symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on Windows")
		}
		dir := t.TempDir()
		source := newSource(t, dir)
		outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
		if err := os.WriteFile(outside, []byte("elsewhere\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.Symlink(outside, rejects); err != nil {
			t.Fatal(err)
		}
		refused(t, source, rejects)
		if info, err := os.Lstat(rejects); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink replaced (lstat %v, err %v)", info, err)
		}
		assertFile(t, outside, "elsewhere\n")
	})

	t.Run("refuses_a_directory", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.Mkdir(rejects, 0o750); err != nil {
			t.Fatal(err)
		}
		refused(t, source, rejects)
	})

	t.Run("refuses_the_source", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		refused(t, source, source)
	})

	t.Run("refuses_a_hardlink_to_the_source", func(t *testing.T) {
		dir := t.TempDir()
		source := newSource(t, dir)
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.Link(source, rejects); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		refused(t, source, rejects)
		if same, err := sameFileForTest(source, rejects); err != nil || !same {
			t.Errorf("hardlink to the source was replaced (same=%v, err %v)", same, err)
		}
	})

	t.Run("failed_replacement_keeps_the_old_file", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a POSIX directory the test cannot write")
		}
		dir := t.TempDir()
		source := newSource(t, t.TempDir())
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.WriteFile(rejects, []byte("old-1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })
		if err := save(source, rejects, r1); err == nil {
			t.Fatal("save into a read-only directory succeeded")
		}
		assertFile(t, rejects, "old-1\n")
	})

	// Ported from pr5202's TestWriteRejectFileHardening (matt wilkie).
	t.Run("rewrite_replaces_a_lax_mode_with_0600_and_keeps_old_records", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("requires POSIX file permission semantics")
		}
		dir := t.TempDir()
		source := newSource(t, dir)
		rejects := filepath.Join(dir, "issues.rejected.jsonl")
		if err := os.WriteFile(rejects, []byte("old-1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(rejects, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := save(source, rejects, r1); err != nil {
			t.Fatalf("save: %v", err)
		}
		assertFile(t, rejects, "old-1\n"+string(r1.Raw)+"\n")
		info, err := os.Stat(rejects)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("rejects file mode = %o, want 0600: a rewrite must not inherit the old lax mode", got)
		}
	})
}

// TestDefaultImportRejectsPath pins the quarantine naming rule (GH#4492):
// beside the source, <stem>.rejected.jsonl for a .jsonl source and the
// suffix appended to any other name. Ported from GLM's test of the same name.
func TestDefaultImportRejectsPath(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{filepath.Join("w", ".beads", "issues.jsonl"), filepath.Join("w", ".beads", "issues.rejected.jsonl")},
		{filepath.Join("w", "backup.txt"), filepath.Join("w", "backup.txt.rejected.jsonl")},
		{filepath.Join("w", "plain"), filepath.Join("w", "plain.rejected.jsonl")},
	} {
		if got := defaultImportRejectsPath(tc.source); got != tc.want {
			t.Errorf("defaultImportRejectsPath(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func sameFileForTest(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

// TestWarnImportInvalidSkippedCapsPerRecordLines pins the bound on the
// skipped-record warning (GH#4492, AC-2): the first importRejectionListLimit
// records are named with their location, the rest are counted, and the
// warning says where the records went. Ported from PR #5202's
// TestReportRejectedRecordsCapsPerRecordWarnings (@maphew).
func TestWarnImportInvalidSkippedCapsPerRecordLines(t *testing.T) {
	var rejected []importRejection
	for i := 1; i <= importRejectionListLimit+5; i++ {
		rejected = append(rejected, importRejection{Line: i, ID: fmt.Sprintf("wc-%d", i), Reason: "invalid status: verify"})
	}
	out := captureStderr(t, func() {
		warnImportInvalidSkipped("in.jsonl", rejected, "in.rejected.jsonl")
	})

	if got := strings.Count(out, ": invalid status: verify"); got != importRejectionListLimit {
		t.Errorf("per-record lines = %d, want %d\ngot:\n%s", got, importRejectionListLimit, out)
	}
	for _, want := range []string{
		"warning: skipped 15 invalid record(s) from in.jsonl:",
		"  in.jsonl:1: wc-1: invalid status: verify",
		"  ... and 5 more",
		"  rejected records saved to in.rejected.jsonl",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("warning missing %q\ngot:\n%s", want, out)
		}
	}
	if strings.Contains(out, "wc-11:") {
		t.Errorf("warning names a record past the limit:\n%s", out)
	}

	// Without a rejects file the warning says the records were not saved.
	out = captureStderr(t, func() {
		warnImportInvalidSkipped("in.jsonl", rejected[:1], "")
	})
	if !strings.Contains(out, "not saved separately; they remain in in.jsonl") {
		t.Errorf("warning without --rejects = %q, want it to say the records remain in the source", out)
	}
}

// vocabularyScreenFixture is an import whose rows would be screened, one of
// them with a status only a configured vocabulary could accept.
var vocabularyScreenFixture = []string{
	`{"_type":"issue","id":"vc-1","title":"valid row","status":"open","priority":2,"issue_type":"task"}`,
	`{"_type":"issue","id":"vc-2","title":"custom status row","status":"verify","priority":2,"issue_type":"task"}`,
}

func writeVocabularyScreenFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(vocabularyScreenFixture, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertNoQuarantine fails if the default quarantine beside source exists.
func assertNoQuarantine(t *testing.T, source string) {
	t.Helper()
	if _, err := os.Stat(defaultImportRejectsPath(source)); !os.IsNotExist(err) {
		t.Errorf("quarantine written (stat err %v), want no file writes", err)
	}
}

// TestScreenImportRecordsVocabularyReadErrorIsNotAVerdict pins the explicit
// import's half of AC-5 (GH#4492): a failed vocabulary read fails the screen
// with the read error, and returns no screen - so no row is written and no
// record is reported invalid. Ported from GLM's
// TestApplyExplicitImportScreenVocabularyErrorAborts.
func TestScreenImportRecordsVocabularyReadErrorIsNotAVerdict(t *testing.T) {
	records, _, err := parseImportJSONLFile(writeVocabularyScreenFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("dolt is on fire")
	screen, err := screenImportRecords(context.Background(), nil, func(context.Context) ([]string, []string, error) {
		return nil, nil, readErr
	}, records, true)
	if !errors.Is(err, readErr) {
		t.Fatalf("screen error = %v, want the vocabulary read error", err)
	}
	if screen != nil {
		t.Errorf("screen = %+v, want none: a read failure is not a record verdict", screen)
	}
}

// TestPlanRecoveryImportVocabularyReadErrorWritesNothing pins AC-5 on the
// recovery plan every recovery importer shares: an unreadable vocabulary
// aborts the plan with the read error, before a quarantine is written and
// without an invalid verdict. Ported from GLM's
// TestRecoveryPlanAbortsWhenVocabularyUnreadable.
func TestPlanRecoveryImportVocabularyReadErrorWritesNothing(t *testing.T) {
	source := writeVocabularyScreenFixture(t)
	readErr := errors.New("resolver offline")
	plan, err := planRecoveryImportFile(context.Background(), func(context.Context) ([]string, []string, error) {
		return nil, nil, readErr
	}, source)
	if !errors.Is(err, readErr) {
		t.Fatalf("plan error = %v, want the vocabulary read error", err)
	}
	if plan != nil {
		t.Errorf("plan = %+v, want none", plan)
	}
	assertNoQuarantine(t, source)
}

// noVocabularyStore has no ImportVocabulary method, directly or underneath.
type noVocabularyStore struct {
	storage.DoltStorage // nil: any call panics
}

// TestPlanRecoveryImportMissingVocabularyCapabilityWritesNothing pins the
// missing-capability abort (GH#4492 AC-5): screening a store that cannot read
// its vocabulary against built-ins alone would set aside every row with a
// configured custom status, so the plan refuses instead, writing nothing.
// Ported from GLM's TestRecoveryPlanAbortsWhenCapabilityMissing.
func TestPlanRecoveryImportMissingVocabularyCapabilityWritesNothing(t *testing.T) {
	source := writeVocabularyScreenFixture(t)
	plan, err := planRecoveryImportFile(context.Background(), storeImportVocabulary(noVocabularyStore{}), source)
	if err == nil || !strings.Contains(err.Error(), "cannot read the custom statuses and types") {
		t.Fatalf("plan error = %v, want the missing-capability refusal", err)
	}
	if plan != nil {
		t.Errorf("plan = %+v, want none", plan)
	}
	assertNoQuarantine(t, source)
}

// TestApplyImportRejectionsDryRunLeavesTheRejectsFileAlone pins AC-7's
// dry-run rule (GH#4492): a --dry-run --skip-invalid --rejects run, clean or
// with records to skip, leaves an earlier run's rejects file byte for byte as
// it was, and a rejected dry run still reports what it would skip. Ported
// from pr5202's TestResolveImportRejectsDryRunLeavesRejectsFileAlone
// (matt wilkie).
func TestApplyImportRejectionsDryRunLeavesTheRejectsFileAlone(t *testing.T) {
	rejected := []importRejection{{Line: 3, ID: "dr-1", Reason: "invalid status: verify", Raw: []byte(`{"id":"dr-1","status":"verify"}`)}}
	for _, tc := range []struct {
		name     string
		rejected []importRejection
	}{
		{name: "clean"},
		{name: "rejected", rejected: rejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
			const keep = "previous run's quarantine content\n"
			if err := os.WriteFile(rejectsPath, []byte(keep), 0o600); err != nil {
				t.Fatal(err)
			}
			result := importResultJSON{DryRun: true}
			if err := applyImportRejections(&result, "fixture.jsonl", tc.rejected, true, rejectsPath); err != nil {
				t.Fatalf("applyImportRejections: %v", err)
			}
			if got, err := os.ReadFile(rejectsPath); err != nil || string(got) != keep {
				t.Errorf("rejects file after a dry run = %q (err %v), want %q unchanged", got, err, keep)
			}
			if result.RejectsFile != "" {
				t.Errorf("rejects_file = %q, want empty: a dry run saves nothing", result.RejectsFile)
			}
			if result.InvalidSkipped != len(tc.rejected) {
				t.Errorf("invalid_skipped = %d, want %d", result.InvalidSkipped, len(tc.rejected))
			}
		})
	}
}

// TestPlanRecoveryImportUndecodableLineReportsPhysicalLocation pins AC-7's
// undecodable-line rule on the recovery plan: a line that cannot decode is
// fatal and named by its physical line - blank lines counted - before any
// write, so no plan is returned and no quarantine exists. Ported from GLM's
// TestRecoveryPlanUndecodableLineReportsPhysicalLocation.
func TestPlanRecoveryImportUndecodableLineReportsPhysicalLocation(t *testing.T) {
	source := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(source, []byte(vocabularyScreenFixture[0]+"\n\n"+vocabularyScreenFixture[1]+"\n"+`{"_type":"issue","id": not json`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := planRecoveryImportFile(context.Background(), func(context.Context) ([]string, []string, error) {
		t.Error("vocabulary read for a file that does not decode")
		return nil, nil, nil
	}, source)
	if err == nil || !strings.Contains(err.Error(), source+":4:") {
		t.Fatalf("plan error = %v, want the physical location %s:4", err, source)
	}
	if plan != nil {
		t.Errorf("plan = %+v, want none", plan)
	}
	assertNoQuarantine(t, source)
}
