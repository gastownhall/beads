// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/ledger_render_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

//go:generate go test . -run TestDivergenceLedgerDocMatchesLedger -update-ledger-doc

var updateLedgerDoc = flag.Bool("update-ledger-doc", false,
	"rewrite engdocs/design/http-divergence-ledger.md from the ledger instead of comparing")

// ledgerDocPath is the shipped artifact, relative to this package directory:
// internal/httpclient/encode -> repo root is three levels up.
var ledgerDocPath = filepath.Join("..", "..", "..",
	"engdocs", "design", "http-divergence-ledger.md")

// TestDivergenceLedgerDocMatchesLedger is the anti-drift half of the ledger's
// well-formedness chain. The bijection gate holds Ledger() honest against the
// wire; this holds the shipped doc honest against Ledger(), so the doc cannot
// describe a divergence set the client does not implement.
//
// It runs in the default (untagged) PR-Core lane with the rest of this package,
// so a ledger edit that forgets to regenerate the doc fails CI on every PR.
// Regenerate with -update-ledger-doc (or go generate ./...).
func TestDivergenceLedgerDocMatchesLedger(t *testing.T) {
	want := RenderLedgerMarkdown()

	if *updateLedgerDoc {
		if err := os.WriteFile(ledgerDocPath, []byte(want), 0o644); err != nil {
			t.Fatalf("writing %s: %v", ledgerDocPath, err)
		}
		t.Logf("regenerated %s", ledgerDocPath)
		return
	}

	got, err := os.ReadFile(ledgerDocPath)
	if err != nil {
		t.Fatalf("reading %s: %v\nregenerate with: go test ./internal/httpclient/encode -run %s -update-ledger-doc",
			ledgerDocPath, err, t.Name())
	}

	if string(got) != want {
		t.Fatalf("engdocs/design/http-divergence-ledger.md is stale: it no longer matches the ledger in encode.Ledger().\n"+
			"The shipped divergence ledger is generated from source and must not be hand-edited.\n"+
			"Regenerate it with:\n"+
			"    go test ./internal/httpclient/encode -run %s -update-ledger-doc\n"+
			"(or: go generate ./internal/httpclient/encode)", t.Name())
	}
}
