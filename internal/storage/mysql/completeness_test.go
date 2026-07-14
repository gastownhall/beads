package mysql

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/conformance"
)

// legitimatelyUnsupported is the EXPLICIT denominator of every storage.DoltStorage
// method the Postgres wedge deliberately does not implement, each with a reason.
//
// The interface-completeness gate (TestInterfaceCompleteness) asserts the
// generated capability shell (unsupported_gen.go) contains EXACTLY these methods
// — no more, no less. This is the guard the red-team had to stand in for: a
// method that silently resolves to the typed-unsupported shell (the class that
// bit us: DeleteIssues, GetCustomStatuses, the batch reads, …) fails this test
// at build time instead of failing a user at runtime. Adding a method to the
// shell without an entry here is a hard error that forces the triage question:
// "implement it in sqlkit, or record here why the wedge legitimately omits it."
var legitimatelyUnsupported = map[string]string{
	// VersionControl — the Dolt commit graph. The wedge's whole premise is
	// "bd on Postgres, history/version-control simply absent".
	"Branch": "VC: Dolt branch", "Checkout": "VC: Dolt checkout",
	"CurrentBranch": "VC", "DeleteBranch": "VC", "ListBranches": "VC",
	// Commit/CommitWithConfig/CommitMergeResolution are NO-OPS on Postgres (data
	// is already durable per-tx), not unsupported — implemented on postgres.Store.
	"CommitExists": "VC", "GetCurrentCommit": "VC: Dolt hash", "Status": "VC",
	"Log": "VC: dolt_log", "Merge": "VC", "GetConflicts": "VC", "ResolveConflicts": "VC",

	// HistoryViewer — Dolt time-travel.
	"History": "history: dolt_log", "AsOf": "history: as-of ref", "Diff": "history: dolt_diff",

	// RemoteStore — Dolt remotes.
	"AddRemote": "remote", "RemoveRemote": "remote", "HasRemote": "remote", "ListRemotes": "remote",
	"Push": "remote", "Pull": "remote", "ForcePush": "remote", "PushRemote": "remote",
	"PullRemote": "remote", "Fetch": "remote", "PushTo": "remote", "PullFrom": "remote",

	// SyncStore — Dolt peer sync.
	"Sync": "sync", "SyncStatus": "sync",

	// FederationStore — Dolt-remote-backed peer table.
	"AddFederationPeer": "federation", "GetFederationPeer": "federation",
	"ListFederationPeers": "federation", "RemoveFederationPeer": "federation",

	// CompactionStore — bound to Dolt commit hashes.
	"CheckEligibility": "compaction", "ApplyCompaction": "compaction",
	"GetTier1Candidates": "compaction", "GetTier2Candidates": "compaction",
	"SnapshotIssue": "compaction", "GetCompactionSnapshot": "compaction", "RestoreFromSnapshot": "compaction",

	// MergeSlot — the Dolt merge-serialization primitive.
	"MergeSlotCreate": "merge-slot", "MergeSlotCheck": "merge-slot",
	"MergeSlotAcquire": "merge-slot", "MergeSlotRelease": "merge-slot",
}

var shellMethodRe = regexp.MustCompile(`func \(unsupportedDoltStorage\) ([A-Za-z0-9]+)\(`)

// TestInterfaceCompleteness is the interface-completeness gate: the generated
// shell must equal the audited legitimatelyUnsupported set, both directions.
func TestInterfaceCompleteness(t *testing.T) {
	src, err := os.ReadFile("unsupported_gen.go")
	if err != nil {
		t.Fatalf("read unsupported_gen.go: %v", err)
	}
	shell := map[string]bool{}
	for _, m := range shellMethodRe.FindAllStringSubmatch(string(src), -1) {
		shell[m[1]] = true
	}
	if len(shell) == 0 {
		t.Fatal("parsed 0 shell methods — regex or file drift")
	}

	// (1) No SILENT gap: every shell method must be explicitly justified.
	var unjustified []string
	for m := range shell {
		if _, ok := legitimatelyUnsupported[m]; !ok {
			unjustified = append(unjustified, m)
		}
	}
	sort.Strings(unjustified)
	for _, m := range unjustified {
		t.Errorf("method %q resolves to the typed-unsupported shell but is NOT in legitimatelyUnsupported: implement it on *sqlkit.Store, or add it here with a reason", m)
	}

	// (2) No STALE allowlist: every justified method must still be in the shell
	// (else it was implemented and the entry should be removed).
	var stale []string
	for m := range legitimatelyUnsupported {
		if !shell[m] {
			stale = append(stale, m)
		}
	}
	sort.Strings(stale)
	for _, m := range stale {
		t.Errorf("method %q is in legitimatelyUnsupported but no longer in the shell (implemented?): remove the allowlist entry", m)
	}
}

// TestUnsupportedContract is the behavioral complement to TestInterfaceCompleteness:
// every allowlisted method must actually return a typed storage.ErrUnsupported when
// called, not panic or return a different error. DB-free — the generated stubs ignore
// their receiver, so a zero-value store answers them.
func TestUnsupportedContract(t *testing.T) {
	conformance.RunUnsupportedContract(t, &Store{}, legitimatelyUnsupported)
}
