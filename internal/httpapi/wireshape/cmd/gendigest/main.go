// Command gendigest regenerates internal/httpapi/wireshape/testdata/golden.json
// from the embedded OpenAPI document and the Go CurrentWireRevision constant.
//
// Run it after a deliberate, revision-bumped wire-shape change — never to make
// a failing TestWireShapeDigest pass on an accidental one. A diff this command
// produces that only adds entries is additive and needs no revision bump; a
// diff that changes or removes an entry without a CurrentWireRevision bump is
// the exact drift the test exists to catch, not something to paper over by
// running this.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/wireshape"
)

func main() {
	initFlag := flag.Bool("init", false, "create golden.json when none exists yet")
	flag.Parse()

	out, err := goldenPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gendigest:", err)
		os.Exit(1)
	}

	digest, err := wireshape.Compute(httpapi.CurrentWireRevision)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gendigest:", err)
		os.Exit(1)
	}

	if err := writeGolden(out, digest, *initFlag); err != nil {
		fmt.Fprintln(os.Stderr, "gendigest:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}

// goldenPath resolves testdata/golden.json relative to this source file, so
// the command works from any cwd, the same way `go generate` directives in
// this repo do.
func goldenPath() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("could not resolve own source path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "golden.json"), nil
}

// writeGolden writes digest to out, under two guards.
//
// First, an EXISTING golden: refuse to overwrite one that would silently
// launder a failing TestWireShapeDigest. A changed or removed entry must come
// with a CurrentWireRevision bump ABOVE what's already committed, never from
// just re-running this command (wireshape.SafeToWrite); a purely additive
// diff always writes, unless digest's wire_revision is LOWER than the
// golden's, which SafeToWrite refuses whatever the entries.
//
// Second, a MISSING golden: refuse to create one unless initFlag is set. A
// golden that is merely absent — the testdata file moved, got deleted by
// accident, or this is being pointed at the wrong path — must not be treated
// as implicit permission to start a fresh history from whatever the working
// tree happens to compute right now; -init is the explicit, one-time act of
// starting that history (the real first run, or a deliberate reset), not
// gendigest's default behavior for "I don't see a file there."
func writeGolden(out string, digest wireshape.Digest, initFlag bool) error {
	// #nosec G304 -- out is this command's own source-relative output path,
	// never request- or argv-influenced; reading it back is the write guard.
	existing, err := os.ReadFile(out)
	switch {
	case err == nil:
		var golden wireshape.Digest
		if err := json.Unmarshal(existing, &golden); err != nil {
			return fmt.Errorf("decode existing golden: %w", err)
		}
		if ok, reason := wireshape.SafeToWrite(golden, digest); !ok {
			return errors.New(reason)
		}
	case os.IsNotExist(err):
		if !initFlag {
			return fmt.Errorf("no golden at %s yet; pass -init to create one (a missing file is not this command's default to fill in)", out)
		}
	default:
		return fmt.Errorf("read existing golden: %w", err)
	}

	blob, err := json.MarshalIndent(digest, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')

	return os.WriteFile(out, blob, 0o600)
}
