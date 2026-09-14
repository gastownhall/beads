package ownershiphandoffv2

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// P7, the one path rule: Request.Root and every path bd journals are recorded
// symlink-resolved at prepare, and every later comparison canonicalises the
// observed side the same way before comparing.
//
// Without it, a workspace reached through a symlink makes bd compare two
// spellings of the same directory and conclude they are different workspaces.
// The consequences are not cosmetic: a COMMITTED journal — the state that means
// "bd owns this scope, carry on" — is refused as unreadable, which bricks every
// ordinary command in that workspace. That is the exact failure this mechanism
// exists to prevent, arriving through the mechanism itself.
//
// macOS gets here for free (/var is a symlink to /private/var, so t.TempDir()
// hands back an unresolved path), which is how CI caught it. A symlinked $HOME,
// an NFS automount, or a symlinked .beads does the same on Linux. This test
// builds the symlink deliberately so the rule is pinned on every platform that
// has symlinks at all, not only where the OS supplies one by accident.
//
// Pattern follows #6544's TestValidateStrictLaunchOptionsResolvesSymlinkedWorkspace.
func symlinkedWorkspace(t *testing.T) (physical, throughLink string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on Windows")
	}
	base := canonicalTempDir(t)
	physical = filepath.Join(base, "physical")
	if err := os.MkdirAll(BeadsDir(physical), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	throughLink = filepath.Join(base, "link")
	if err := os.Symlink(physical, throughLink); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	// Guard against a platform where the two spell the same: the test would
	// pass without exercising anything.
	if throughLink == physical {
		t.Skip("symlink and target have the same path")
	}
	return physical, throughLink
}

// A committed journal reached through a symlinked path must admit. This is the
// brick: refuse it and every bd command in that workspace stops working, with a
// message about a journal the operator never wrote.
func TestFenceAdmitsACommittedJournalThroughASymlinkedPath(t *testing.T) {
	physical, throughLink := symlinkedWorkspace(t)

	// The journal records the spelling the caller used. That is the realistic
	// case and the one macOS CI hit: t.TempDir() there hands back /var/..., the
	// request carries it, and the fence later resolves to /private/var/... . A
	// comparison that is a string match rather than a path match then decides
	// the journal belongs to some other workspace.
	j := journalAt(t, throughLink, PhaseCommitted)
	if err := save(JournalPath(physical), j); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := CheckNormalOpen(BeadsDir(physical)); err != nil {
		t.Fatalf("fence refused a committed journal through its physical path: %v", err)
	}
	if err := CheckNormalOpen(BeadsDir(throughLink)); err != nil {
		t.Fatalf("fence refused a committed journal through a symlinked path: %v", err)
	}

	// And the mirror: a journal recording the resolved spelling, reached
	// through the link. Both directions must work, because bd does not control
	// which spelling reaches it.
	if err := save(JournalPath(physical), journalAt(t, physical, PhaseCommitted)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := CheckNormalOpen(BeadsDir(throughLink)); err != nil {
		t.Fatalf("fence refused a resolved committed journal reached through a symlink: %v", err)
	}
}

// A journal that genuinely belongs to a different workspace is still refused as
// unreadable. Canonicalising the comparison must not soften the check into
// accepting anything.
func TestFenceStillRefusesAForeignJournal(t *testing.T) {
	physical, _ := symlinkedWorkspace(t)
	elsewhere := canonicalTempDir(t)

	if err := save(JournalPath(physical), journalAt(t, elsewhere, PhaseCommitted)); err != nil {
		t.Fatalf("save: %v", err)
	}
	err := CheckNormalOpen(BeadsDir(physical))
	if err == nil {
		t.Fatal("fence admitted a journal belonging to another workspace")
	}
	if code := ErrorCode(err); code != CodeJournalUnreadable {
		t.Fatalf("foreign journal reported %q, want %q", code, CodeJournalUnreadable)
	}
}

// A mid-flight refusal through a symlinked path must still be a FENCE, so
// IsFenced answers true and callers can tell "a handoff owns this workspace"
// from "this workspace is broken". Reporting it as journal_unreadable sends the
// operator looking for corruption that is not there.
func TestFenceReportsAMidFlightRefusalThroughASymlinkAsFenced(t *testing.T) {
	physical, throughLink := symlinkedWorkspace(t)

	for _, phase := range []Phase{PhasePrepared, PhaseVerified, PhaseLegacyConfigRestored} {
		t.Run(string(phase), func(t *testing.T) {
			if err := save(JournalPath(physical), journalAt(t, throughLink, phase)); err != nil {
				t.Fatalf("save: %v", err)
			}
			err := CheckNormalOpen(BeadsDir(throughLink))
			if err == nil {
				t.Fatalf("fence admitted an in-flight handoff at %s", phase)
			}
			if !IsFenced(err) {
				t.Fatalf("refusal at %s through a symlink is not reported as a fence: %v", phase, err)
			}
			if code := ErrorCode(err); code != CodePhaseOrder {
				t.Fatalf("refusal at %s is %q, want %q", phase, code, CodePhaseOrder)
			}
		})
	}
}

// Resuming through the symlink is the same handoff, not a conflicting one. A
// caller that reached the workspace one way at prepare and the other way at
// legacy-gone must not be told its request names a different scope.
func TestResumeThroughASymlinkIsNotAnIdentityConflict(t *testing.T) {
	physical, throughLink := symlinkedWorkspace(t)

	// The journal holds the unresolved spelling; the request will hold the
	// resolved one. sameScope has to see one workspace, not two.
	if err := save(JournalPath(physical), journalAt(t, throughLink, PhasePrepared)); err != nil {
		t.Fatalf("save: %v", err)
	}
	opts := Options{
		Root:      throughLink,
		Database:  "scope_db",
		Workspace: "workspace-uuid",
		Endpoint:  Endpoint{Host: "127.0.0.1", Port: 3307},
		BDVersion: "test",
	}
	result, err := Run(context.Background(), VerbLegacyGone, opts)
	if code := ErrorCode(err); code == CodeIdentityConflict {
		t.Fatalf("resuming through a symlinked path was refused as a different scope: %v", err)
	}
	// It will refuse for a real reason — nothing is listening on 3307 here —
	// but the journal must have been adopted, not rejected.
	if result.Phase != PhasePrepared {
		t.Fatalf("the journal was not adopted through the symlink: phase %q", result.Phase)
	}
	if result.Request.Root != physical {
		t.Fatalf("the request root is %q, want the resolved %q", result.Request.Root, physical)
	}
}

// prepare records the resolved root, whatever spelling the caller used. Every
// later comparison rests on this, so it is asserted directly rather than only
// through the behaviour above.
func TestBuildRequestRecordsTheResolvedRoot(t *testing.T) {
	physical, throughLink := symlinkedWorkspace(t)

	req, err := buildRequest(Options{
		Root:      throughLink,
		Database:  "scope_db",
		Workspace: "w",
		Endpoint:  Endpoint{Host: "127.0.0.1", Port: 3307},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.Root != physical {
		t.Fatalf("request root is %q, want the resolved %q", req.Root, physical)
	}
}

// The helper itself: resolving is best-effort, because a path that does not
// exist yet still has to compare equal to itself.
func TestCanonicalPathFallsBackToCleanForAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not", "created", "yet")
	if got := canonicalPath(missing); got != filepath.Clean(missing) {
		t.Fatalf("canonicalPath(%q) = %q, want the cleaned path", missing, got)
	}
	if canonicalPath("") != "" {
		t.Fatal("canonicalPath rewrote the empty path")
	}
}

// samePath is the comparison every caller must use. Two spellings of one
// directory are the same directory.
func TestSamePathSeesThroughASymlink(t *testing.T) {
	physical, throughLink := symlinkedWorkspace(t)
	if !samePath(physical, throughLink) {
		t.Fatalf("samePath(%q, %q) is false", physical, throughLink)
	}
	if samePath(physical, filepath.Join(physical, "elsewhere")) {
		t.Fatal("samePath conflated a directory with a child of it")
	}
}
