package corpus_test

import (
	"os"
	"testing"
)

// corpusTierLinked reports whether this test binary links the corpus tier.
// harness_test.go, which builds only under cgo like every other file in this
// package, sets it from its init.
var corpusTierLinked bool

// TestCorpusTierIsLinkedWhenRequired is the one corpus check that builds
// without cgo. skipUnlessEmbeddedDolt turns a missing BEADS_TEST_EMBEDDED_DOLT
// into a failure under BEADS_HTTP_TEST_REQUIRED=1, but it lives in a cgo file:
// with cgo off the go tool drops every other file in this package, so no
// corpus test exists to fail, and a required run would pass having run nothing.
func TestCorpusTierIsLinkedWhenRequired(t *testing.T) {
	if os.Getenv("BEADS_HTTP_TEST_REQUIRED") == "1" && !corpusTierLinked {
		t.Fatal("BEADS_HTTP_TEST_REQUIRED=1 but this test binary was built without cgo, which drops " +
			"the http command corpus (every other file in this package); build it with CGO_ENABLED=1")
	}
}
