package main

import (
	"encoding/json"
	"strings"
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

func TestHistoryDirectOnlyRefusalContract(t *testing.T) {
	oldProvider := uowProvider
	oldJSON := jsonOutput
	t.Cleanup(func() { uowProvider = oldProvider; jsonOutput = oldJSON })
	for _, path := range []string{"branch", "conflicts", "repo", "federation", "vc", "flatten", "dolt push", "dolt pull", "dolt commit", "dolt remote add", "sync"} {
		parts := strings.Split(path, " ")
		root := &cobra.Command{Use: "bd"}
		cmd := &cobra.Command{Use: parts[0]}
		root.AddCommand(cmd)
		for _, part := range parts[1:] {
			child := &cobra.Command{Use: part}
			cmd.AddCommand(child)
			cmd = child
		}
		uowProvider = nil
		jsonOutput = true
		out := captureStdout(t, func() error { _ = validateProxyMaintenanceBeforeProvider(cmd); return nil })
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil || got["code"] == nil {
			t.Fatalf("%s refusal = %q (%v)", path, out, err)
		}
		if uowProvider != nil {
			t.Fatal("refusal initialized provider")
		}
	}
}

func TestHistoryNestedFrontDoorsRefuseAndSupportedPathsPass(t *testing.T) {
	for _, path := range []string{"vc merge", "vc commit", "repo add", "conflicts resolve", "federation sync", "dolt remote remove"} {
		parts := strings.Split(path, " ")
		root := &cobra.Command{Use: "bd"}
		cmd := &cobra.Command{Use: parts[0]}
		root.AddCommand(cmd)
		for _, part := range parts[1:] {
			child := &cobra.Command{Use: part}
			cmd.AddCommand(child)
			cmd = child
		}
		err := validateProxyMaintenanceBeforeProvider(cmd)
		if path == "dolt remote remove" {
			if err != nil {
				t.Fatalf("supported %s refused: %v", path, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("direct-only %s unexpectedly allowed", path)
		}
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Fatalf("%s exit=%v, want 1", path, err)
		}
	}
	root := &cobra.Command{Use: "bd"}
	history := &cobra.Command{Use: "history"}
	root.AddCommand(history)
	if err := validateProxyMaintenanceBeforeProvider(history); err != nil {
		t.Fatalf("history --events supported path refused: %v", err)
	}
}
