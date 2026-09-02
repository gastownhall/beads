package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestHistoryCapabilityMatrixExactPaths(t *testing.T) {
	if got, ok := LookupHistoryCapability("history"); !ok || got != HistoryProxySupported {
		t.Fatalf("history = %q, %v; want proxy-supported", got, ok)
	}
	for _, path := range []string{"branch", "conflicts", "repo", "federation", "vc", "flatten", "dolt push", "dolt pull", "dolt commit", "dolt remote", "sync"} {
		if got, ok := LookupHistoryCapability(path); !ok || got != HistoryDirectOnly {
			t.Errorf("%q = %q, %v; want direct-only", path, got, ok)
		}
	}
	if got, ok := LookupHistoryCapability("dolt remote remove"); !ok || got != HistoryProxySupported {
		t.Fatalf("dolt remote remove = %q, %v; want proxy-supported", got, ok)
	}
	for _, path := range []string{"dolt remote add", "dolt remote list", "dolt remote reset-data"} {
		if got, ok := LookupHistoryCapability(path); !ok || got != HistoryDirectOnly {
			t.Errorf("%q = %q, %v; want direct-only", path, got, ok)
		}
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
