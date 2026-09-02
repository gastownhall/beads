package main

import (
	"testing"

	"github.com/spf13/cobra"
)

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

func TestSyncRefusesBeforeProvider(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	cmd := &cobra.Command{Use: "sync"}
	root.AddCommand(cmd)
	err := validateProxyMaintenanceBeforeProvider(cmd)
	if err == nil {
		t.Fatalf("sync refusal = %#v", err)
	}
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("sync exit = %#v", err)
	}
}
