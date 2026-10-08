package httpclient

import (
	"os"
	"testing"
)

// servedTierLinked reports whether this test binary links the served tier.
// served_harness_test.go, which builds only under cgo like every
// served_*_test.go file, sets it from its init.
var servedTierLinked bool

// TestServedTierIsLinkedWhenRequired is the one served-tier check that builds
// without cgo. skipUnlessEmbeddedDolt turns a missing BEADS_TEST_EMBEDDED_DOLT
// into a failure under BEADS_HTTP_TEST_REQUIRED=1, but it lives in a cgo file:
// with cgo off the go tool drops every served_*_test.go file, so no served test
// exists to fail, and a required run would pass having served nothing.
func TestServedTierIsLinkedWhenRequired(t *testing.T) {
	if os.Getenv("BEADS_HTTP_TEST_REQUIRED") == "1" && !servedTierLinked {
		t.Fatal("BEADS_HTTP_TEST_REQUIRED=1 but this test binary was built without cgo, which drops " +
			"the served-surface conformance tier (served_*_test.go); build it with CGO_ENABLED=1")
	}
}
