package main

import "testing"

func TestHistoryCapabilityMatrixExactPaths(t *testing.T) {
	for _, path := range []string{"branch", "conflicts", "repo", "federation", "vc", "flatten", "dolt push", "dolt pull", "dolt commit", "dolt remote", "sync"} {
		if got, ok := LookupHistoryCapability(path); !ok || got != HistoryDirectOnly {
			t.Errorf("%q = %q, %v; want direct-only", path, got, ok)
		}
	}
	if _, ok := LookupHistoryCapability("dolt remote remove"); ok {
		t.Fatal("nested unknown path must not inherit parent capability")
	}
}
