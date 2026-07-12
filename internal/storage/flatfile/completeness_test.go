package flatfile

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/conformance"
)

// legitimatelyUnsupported is the EXPLICIT denominator of every storage.DoltStorage
// method the flat-file backend deliberately does not implement, each with a reason.
//
// It is deliberately tiny: flat-file implements version control, history, remotes,
// and sync on plain git (gitops.go), merge slots and metadata slots on JSON files
// (extensions.go), and compaction as documented no-ops. The only genuine omission
// is FederationStore — the Dolt-remote-backed peer registry has no flat-file
// analog because peers are just git remotes.
//
// The interface-completeness gate (TestInterfaceCompleteness) asserts the
// generated capability shell (unsupported_gen.go) contains EXACTLY these methods
// — no more, no less. A method that silently resolves to the typed-unsupported
// shell fails this test at build time instead of failing a user at runtime.
// Adding a method to the shell without an entry here is a hard error that forces
// the triage question: "implement it on *FlatFileStore, or record here why the
// backend legitimately omits it."
var legitimatelyUnsupported = map[string]string{
	// FederationStore — the Dolt-remote-backed peer table. Flat-file peers are
	// plain git remotes (AddRemote/Push/Pull/Sync are implemented in gitops.go),
	// so a separate federation peer registry has no analog.
	"AddFederationPeer":    "federation: peers are git remotes",
	"GetFederationPeer":    "federation: peers are git remotes",
	"ListFederationPeers":  "federation: peers are git remotes",
	"RemoveFederationPeer": "federation: peers are git remotes",
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
		t.Errorf("method %q resolves to the typed-unsupported shell but is NOT in legitimatelyUnsupported: implement it on *FlatFileStore, or add it here with a reason", m)
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
// called, not panic or return a different error. Disk-free — the generated stubs
// ignore their receiver, so a zero-value store answers them.
func TestUnsupportedContract(t *testing.T) {
	conformance.RunUnsupportedContract(t, &FlatFileStore{}, legitimatelyUnsupported)
}
