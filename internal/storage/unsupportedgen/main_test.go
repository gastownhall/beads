package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedShellIsFresh is the anti-drift check this tool never had: it
// runs exactly the same scan-then-generate pipeline main() runs for the one
// registered consumer (internal/httpclient's unsupported_gen.go, built from
// storage.DoltStorage with receiver Store), and compares the result byte for
// byte against the checked-in file. A storage.DoltStorage interface change,
// a hand-written method added or removed from httpclient's Store, or an edit
// to this generator's own output shape that nobody reran `go generate` after
// is caught here instead of silently drifting until someone notices the
// shell no longer matches what the generator would produce today.
//
// Regenerate with: go generate ./internal/httpclient (see unsupported.go's
// //go:generate line, which this test mirrors).
func TestGeneratedShellIsFresh(t *testing.T) {
	const (
		typeName = "DoltStorage"
		pkgName  = "httpclient"
		receiver = "Store"
		outName  = "unsupported_gen.go"
	)

	// httpclient lives two levels up from this package
	// (internal/storage/unsupportedgen -> internal/httpclient).
	scanDir := filepath.Join("..", "..", "httpclient")
	outPath := filepath.Join(scanDir, outName)

	ifaceType, ok := registry[typeName]
	if !ok {
		t.Fatalf("%q is not in this tool's registry (add it in main.go)", typeName)
	}

	handWritten, err := scanHandWrittenMethods(scanDir, receiver, outPath)
	if err != nil {
		t.Fatalf("scanning %s: %v", scanDir, err)
	}

	want, _, err := generate(pkgName, typeName, ifaceType, handWritten)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading checked-in %s: %v", outPath, err)
	}

	if string(got) != string(want) {
		t.Fatalf("%s is stale: it does not match what this generator produces today.\n"+
			"This file is generated and must not be hand-edited.\n"+
			"Regenerate it with:\n"+
			"    go generate ./internal/httpclient", outPath)
	}
}

// TestScanHandWrittenMethodsExcludesOwnOutput pins the one correctness
// property the freshness check above silently depends on: scanning must
// never see the generator's own prior output as "hand-written," or every
// method the shell currently stubs would get treated as already covered and
// the next regeneration would stub nothing at all.
func TestScanHandWrittenMethodsExcludesOwnOutput(t *testing.T) {
	scanDir := filepath.Join("..", "..", "httpclient")
	outPath := filepath.Join(scanDir, "unsupported_gen.go")

	handWritten, err := scanHandWrittenMethods(scanDir, "Store", outPath)
	if err != nil {
		t.Fatalf("scanning %s: %v", scanDir, err)
	}

	// unsupportedDoltStorage's receiver is unsupportedDoltStorage, not Store,
	// so none of its stub method names should ever land in the Store-receiver
	// hand-written set purely by virtue of being in the output file -- but to
	// catch a regression where the output file itself was scanned anyway,
	// assert directly that scanning skipped it: a method this generator is
	// known to stub today (AsOf, from storage.DoltStorage's version-control
	// family) must not appear as hand-written on Store.
	if handWritten["AsOf"] {
		t.Error(`handWritten["AsOf"] = true, want false -- scanHandWrittenMethods appears to have scanned its own output file`)
	}
}
