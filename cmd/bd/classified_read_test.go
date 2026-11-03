package main

import (
	"fmt"
	"testing"
)

// TestClassifiedReadOpen walks all 16 input combinations: only a
// classification-only read with no strict --readonly, no preview, and no
// migration freeze may run the defer-wake sweep (be-vbhpf, bd-6zf0q5).
func TestClassifiedReadOpen(t *testing.T) {
	for _, policyReadOnly := range []bool{false, true} {
		for _, readonlyMode := range []bool{false, true} {
			for _, previewMode := range []bool{false, true} {
				for _, frozen := range []bool{false, true} {
					want := [4]bool{policyReadOnly, readonlyMode, previewMode, frozen} == [4]bool{true, false, false, false}
					name := fmt.Sprintf("policy=%t/readonly=%t/preview=%t/frozen=%t", policyReadOnly, readonlyMode, previewMode, frozen)
					t.Run(name, func(t *testing.T) {
						if got := classifiedReadOpen(policyReadOnly, readonlyMode, previewMode, frozen); got != want {
							t.Errorf("classifiedReadOpen(%t, %t, %t, %t) = %t, want %t", policyReadOnly, readonlyMode, previewMode, frozen, got, want)
						}
					})
				}
			}
		}
	}
}
