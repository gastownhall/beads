package ownershiphandoffv2

import (
	"errors"
	"os"
	"path/filepath"
)

// CheckNormalOpen admits ordinary bd store initialization only when no handoff
// journal exists, or the journal has durably settled — committed, so bd owns
// the scope, or rolled back and archived, so the caller does.
//
// A journal in any other state is a lifecycle fence, not a hint to disable
// auto-start. Even a read command must not open, migrate or adopt a server
// while an explicit handoff owns the transition: between legacy-gone and
// commit, the scope has no settled owner, and a bd that opened it anyway would
// be the second process to decide what the workspace's server should be.
//
// The fence always has an exit. rollback-finish is re-runnable while it
// refuses, so a caller whose server came back late can clear a
// legacy_config_restored journal by running it again.
func CheckNormalOpen(beadsDir string) error {
	if beadsDir == "" {
		return nil
	}
	clean := filepath.Clean(beadsDir)
	journalPath := filepath.Join(clean, JournalName)
	if _, err := os.Lstat(journalPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return codedf(CodeJournalUnreadable, "stat ownership handoff journal: %w", err)
	}
	// Resolve through symlinks before trusting the path: a journal reached via
	// a symlinked .beads belongs to the physical workspace, and that is the one
	// whose root the request must name.
	physical, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return codedf(CodeJournalUnreadable, "resolve ownership handoff workspace: %w", err)
	}
	journalPath = filepath.Join(physical, JournalName)
	j, err := Load(journalPath)
	if err != nil {
		if ErrorCode(err) == CodeUnsupportedJournalVersion {
			return err
		}
		return coded(CodeJournalUnreadable, err)
	}
	root := filepath.Dir(physical)
	if j.Request.Root != root {
		return codedf(CodeJournalUnreadable,
			"ownership handoff journal names root %q but sits in %q", j.Request.Root, root)
	}
	switch j.Phase {
	case PhaseCommitted:
		// bd owns the scope. The journal stays on disk because the caller's
		// projection reads it to classify the scope as no longer theirs.
		return nil
	case PhaseRolledBack:
		// rollback-finish archives as its last act, so a live journal in this
		// phase means that archive lost a race or an older build wrote it.
		// Re-prove the restore before admitting, then archive it: an artifact
		// set that never matched proves nothing and stays refused.
		if err := assertRestored(root, j.Snapshot); err != nil {
			return coded(CodeJournalUnreadable, err)
		}
		// A workspace this process cannot write does not un-prove what the
		// comparison above just proved, so the open is admitted either way.
		_ = archiveRolledBackJournal(journalPath, j)
		return nil
	default:
		return codedf(CodePhaseOrder,
			"ownership handoff is %s for this workspace; finish or roll back the handoff before using bd here", j.Phase)
	}
}

// IsFenced reports whether err is CheckNormalOpen refusing an open. Callers
// that must distinguish "the handoff fence is up" from "this workspace is
// broken" branch on it.
func IsFenced(err error) bool {
	if err == nil {
		return false
	}
	var c CodedError
	if !errors.As(err, &c) {
		return false
	}
	return c.Code == CodePhaseOrder || c.Code == CodeUnsupportedJournalVersion
}
