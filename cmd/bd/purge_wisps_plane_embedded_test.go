//go:build cgo

package main

import (
	"os"
	"testing"
)

func TestEmbeddedPurgeWispsPlaneRetention(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pwp")
	runPurgeRetentionScenario(t, embeddedPurgeScenarioRunner(bd, dir))
}
