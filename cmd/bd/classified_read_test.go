package main

import (
	"fmt"
	"testing"
)

// TestClassifiedReadOpen covers all 16 cells: only a classification-only read
// with no strict --readonly, no preview, and no migration freeze may sweep.
func TestClassifiedReadOpen(t *testing.T) {
	cases := []struct {
		policyReadOnly, readonlyMode, previewMode, frozen, want bool
	}{
		// policyReadOnly, readonlyMode, previewMode, frozen, want
		{false, false, false, false, false},
		{false, false, false, true, false},
		{false, false, true, false, false},
		{false, false, true, true, false},
		{false, true, false, false, false},
		{false, true, false, true, false},
		{false, true, true, false, false},
		{false, true, true, true, false},
		{true, false, false, false, true},
		{true, false, false, true, false},
		{true, false, true, false, false},
		{true, false, true, true, false},
		{true, true, false, false, false},
		{true, true, false, true, false},
		{true, true, true, false, false},
		{true, true, true, true, false},
	}
	for _, tc := range cases {
		name := fmt.Sprintf("policy=%t/readonly=%t/preview=%t/frozen=%t", tc.policyReadOnly, tc.readonlyMode, tc.previewMode, tc.frozen)
		t.Run(name, func(t *testing.T) {
			if got := classifiedReadOpen(tc.policyReadOnly, tc.readonlyMode, tc.previewMode, tc.frozen); got != tc.want {
				t.Errorf("classifiedReadOpen = %t, want %t", got, tc.want)
			}
		})
	}
}
