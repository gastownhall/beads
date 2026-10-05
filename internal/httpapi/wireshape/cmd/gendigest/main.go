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
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/wireshape"
)

func main() {
	digest, err := wireshape.Compute(httpapi.CurrentWireRevision)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gendigest:", err)
		os.Exit(1)
	}
	blob, err := json.MarshalIndent(digest, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gendigest:", err)
		os.Exit(1)
	}
	blob = append(blob, '\n')

	// Resolve relative to this source file so the command works from any cwd,
	// the same way `go generate` directives in this repo do.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "gendigest: could not resolve own source path")
		os.Exit(1)
	}
	out := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "golden.json")
	if err := os.WriteFile(out, blob, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "gendigest:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}
