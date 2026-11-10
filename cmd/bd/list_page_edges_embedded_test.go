//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedListPageEdges runs the page-edge parity proof and the direct
// route's edge-read pin (list_page_edges_store_test.go) against embedded Dolt.
// The two share one seeded store; the Dolt-server lane runs them through
// TestListDependencyRecordsMatchesTheStoreWideRead and
// TestListTextRenderingsReadTheListedIssuesEdgesOnly.
func TestEmbeddedListPageEdges(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	f := seedListEdgeStore(t, openEmbeddedListEdgeStore(t))
	t.Run("records match the store-wide read", func(t *testing.T) {
		checkListDependencyRecordsParity(t, f)
	})
	t.Run("text renderings read the listed issues' edges", func(t *testing.T) {
		checkListTextRenderingsReadListedEdges(t, f)
	})
}
