//go:build cgo

package main

import (
	"os"
	"testing"
)

func TestEmbeddedTypesAgreeWithCreate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tac")
	assertTypesAgreeWithCreate(t, func(t *testing.T, args ...string) ([]byte, error) {
		return bdRunWithFlockRetry(t, bd, dir, args...)
	}, beadsDir)
}
