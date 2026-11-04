//go:build cgo

package bdhttp_test

import (
	"slices"
	"strings"
	"testing"
)

// TestE2E_CloseRereadsItsOwnWrite (mc-5dkzp): the per-command read memo must
// never answer a read with a row from before this command's own write. Against
// the MC server's handshake (no issues.close.autoCloseMolecule) bd close runs
// the molecule auto-close check client side after the close, and that check
// re-reads the closed row. Two halves hold it:
//
//   - The wire: the re-read reaches the server after the close's write. A memo
//     that survived the write answers it from the pre-close response, and no
//     request is made.
//   - What the user sees: a lone molecule closed by the command is its own
//     root. Read fresh it is closed and nothing is said; where the check reads
//     the root's status, a memoized pre-close row reads as a completed,
//     still-open molecule and the close prints a false "completed molecule"
//     note.
//
// A hook-payload test cannot guard this: the http write roles fire hooks from
// the write's own response, never from a re-read.
func TestE2E_CloseRereadsItsOwnWrite(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	proxyAddr, counter := startMaskingProxy(t, addr, mcContextMask)
	f := newHTTPFixture(t, bin, proxyAddr)
	mol := f.create("lonely molecule", "-t", "molecule")
	// bd close resolves and reads the row before it writes, so a memo that
	// survived the write would still hold the open row.
	counter.reset()
	res := f.mustRun(nil, "close", mol)
	uris := counter.uriSnapshot()
	t.Logf("bd close: %s", strings.Join(uris, ", "))
	if strings.Contains(res.stderr, "completed molecule") {
		t.Fatalf("bd close re-read its own closed row from before the write and named it a completed, still-open molecule: %s", res.stderr)
	}
	write := slices.Index(uris, "POST /v0/beads/issues:batchClose")
	if write < 0 {
		t.Fatalf("bd close %s sent no batchClose: %q", mol, uris)
	}
	if !slices.Contains(uris[write+1:], "GET /v0/beads/issues/"+mol) {
		t.Fatalf("bd close %s did not re-read the row from the server after closing it (%q): a read after the command's own write was answered from before it", mol, uris)
	}
}
