package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestProxyCapabilityMatrix(t *testing.T) {
	for _, cap := range []ProxyCapability{ProxyCapReadonly, ProxyCapMaxRows} {
		err := AssertProxyCapability(ProxyModeProxied, cap)
		if err == nil {
			t.Errorf("%s unexpectedly honored", cap)
		}
		var typed *ProxyCapabilityError
		if !errors.As(err, &typed) || typed.Code == "" || typed.ExitCode != 1 || typed.Mutates {
			t.Errorf("%s error = %#v, want stable non-mutating refusal", cap, err)
		}
	}
	for _, tc := range []struct {
		cap  ProxyCapability
		want string
	}{
		{ProxyCapWatch, "watch mode not supported in proxied-server mode"},
		{ProxyCapRepo, "--repo is not supported with --proxied-server"},
	} {
		err := AssertProxyCapability(ProxyModeProxied, tc.cap)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s error = %v, want %q", tc.cap, err, tc.want)
		}
	}
}

func TestProxyCapabilityRowsCoverTopologies(t *testing.T) {
	for _, topology := range []ProxyTopology{ProxyTopologyManagedLocal, ProxyTopologyExternalTCP, ProxyTopologyExternalUnix} {
		for _, arg := range []string{"--readonly", "--max-rows", "--watch", "--repo"} {
			if _, ok := LookupProxyCapabilityAt("", arg, ProxyModeProxied, topology); !ok {
				t.Errorf("missing proxied row topology=%s argument=%s", topology, arg)
			}
		}
	}
}

func TestProxyCapabilityCommandRows(t *testing.T) {
	cases := []struct {
		command, argument string
		outcome           ProxyCapabilityOutcome
	}{
		{"list", "--max-rows", ProxyOutcomeHonored},
		{"dep tree", "--max-rows", ProxyOutcomeHonored},
		{"ready", "--max-rows", ProxyOutcomeRefused},
		{"graph", "--max-rows", ProxyOutcomeRefused},
		{"find-duplicates", "--max-rows", ProxyOutcomeRefused},
		{"show", "--watch", ProxyOutcomeRefused},
		{"list", "--watch", ProxyOutcomeHonored},
	}
	for _, topology := range []ProxyTopology{ProxyTopologyManagedLocal, ProxyTopologyExternalTCP, ProxyTopologyExternalUnix} {
		for _, tc := range cases {
			rule, ok := LookupProxyCapabilityAt(tc.command, tc.argument, ProxyModeProxied, topology)
			if !ok || rule.Outcome != tc.outcome {
				t.Errorf("topology=%s %s %s outcome=%q ok=%v, want %q", topology, tc.command, tc.argument, rule.Outcome, ok, tc.outcome)
			}
		}
	}
}

func TestProxyMaintenanceNestedPathsRefuseBeforeProvider(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, path := range []string{"backup init", "backup status", "migrate sync", "gate discover"} {
		parts := strings.Split(path, " ")
		root := &cobra.Command{Use: "bd"}
		parent := &cobra.Command{Use: parts[0]}
		child := &cobra.Command{Use: parts[1]}
		root.AddCommand(parent)
		parent.AddCommand(child)
		out := captureStdout(t, func() error {
			_ = validateProxyMaintenanceBeforeProvider(child)
			return nil
		})
		if !strings.Contains(out, `"code":`) {
			t.Errorf("%s produced no typed refusal: %s", path, out)
		}
	}
}

func TestProxyFormulaSwarmMergeSlotRefusals(t *testing.T) {
	for _, path := range []string{"cook", "ship", "swarm create", "swarm list", "merge-slot create", "merge-slot check", "merge-slot acquire", "merge-slot release"} {
		parts := strings.Split(path, " ")
		root := &cobra.Command{Use: "bd"}
		cmd := &cobra.Command{Use: parts[0]}
		root.AddCommand(cmd)
		for _, childName := range parts[1:] {
			child := &cobra.Command{Use: childName}
			cmd.AddCommand(child)
			cmd = child
		}
		err := validateProxyMaintenanceBeforeProvider(cmd)
		if err == nil {
			t.Fatalf("%s unexpectedly allowed", path)
		}
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Fatalf("%s exit=%v, want 1", path, err)
		}
	}
}

func TestProxyWorkflowRefusalContractAndNoMutation(t *testing.T) {
	cases := []struct {
		path, code, message string
	}{
		{"cook", "proxy.formula.unsupported", "cook is not supported in proxied-server mode"},
		{"ship", "proxy.formula.unsupported", "ship is not supported in proxied-server mode"},
		{"swarm create", "proxy.swarm.unsupported", "swarm create is not supported in proxied-server mode"},
		{"merge-slot acquire", "proxy.merge_slot.unsupported", "merge-slot acquire is not supported in proxied-server mode"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			parts := strings.Split(tc.path, " ")
			root := &cobra.Command{Use: "bd"}
			cmd := &cobra.Command{Use: parts[0]}
			root.AddCommand(cmd)
			if tc.path == "cook" {
				cmd.Flags().Bool("persist", false, "")
				_ = cmd.Flags().Set("persist", "true")
			}
			for _, name := range parts[1:] {
				child := &cobra.Command{Use: name}
				cmd.AddCommand(child)
				cmd = child
			}
			row, ok := lookupProxyMaintenanceRuleForTest(tc.path)
			if !ok || row.Code != tc.code || row.Message != tc.message || row.ExitCode != 1 || row.Mutates {
				t.Fatalf("row = %#v, ok=%v", row, ok)
			}
			var typed *ProxyCapabilityError = &ProxyCapabilityError{Code: row.Code, Message: row.Message, ExitCode: row.ExitCode, Mutates: row.Mutates}
			if typed.Code != tc.code || typed.Message != tc.message || typed.ExitCode != 1 || typed.Mutates {
				t.Fatalf("typed refusal = %#v", typed)
			}
			dir := t.TempDir()
			before := []byte("unchanged\n")
			for _, name := range []string{"issues.jsonl", "config.yaml", "events.jsonl"} {
				if err := os.WriteFile(dir+"/"+name, before, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			oldProvider := uowProvider
			uowProvider = nil
			t.Cleanup(func() { uowProvider = oldProvider })
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			// commandDidWrite is a process-wide latch that any earlier test in
			// this package may already have set, so it has to be baselined
			// here or the assertion below reports another test's write.
			oldDidWrite := commandDidWrite.Load()
			commandDidWrite.Store(false)
			t.Cleanup(func() { commandDidWrite.Store(oldDidWrite) })
			out := captureStdout(t, func() error { _ = validateProxyMaintenanceBeforeProvider(cmd); return nil })
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil || got["code"] != tc.code || got["error"] != tc.message {
				t.Fatalf("JSON refusal = %q (%v)", out, err)
			}
			if uowProvider != nil || commandDidWrite.Load() {
				t.Fatal("refusal initialized provider or marked a write")
			}
			for _, name := range []string{"issues.jsonl", "config.yaml", "events.jsonl"} {
				gotBytes, err := os.ReadFile(dir + "/" + name)
				if err != nil || !bytes.Equal(gotBytes, before) {
					t.Fatalf("%s mutated: %v", name, err)
				}
			}
		})
	}
}

func lookupProxyMaintenanceRuleForTest(path string) (proxyCapabilityRule, bool) {
	rule, ok := proxyMaintenanceRefusals[path]
	return rule, ok
}

func TestProxyMaintenanceRefusalLeavesFilesUntouched(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	migrate := &cobra.Command{Use: "migrate"}
	hooks := &cobra.Command{Use: "hooks"}
	root.AddCommand(migrate)
	migrate.AddCommand(hooks)
	before := []byte("hooks-state")
	path := t.TempDir() + "/.local_version"
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	oldProvider := uowProvider
	uowProvider = nil
	t.Cleanup(func() { uowProvider = oldProvider })
	err := validateProxyMaintenanceBeforeProvider(hooks)
	if err == nil {
		t.Fatal("expected typed maintenance refusal")
	}
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("exit = %v, want 1", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("refusal mutated %s", path)
	}
}

func TestProxyCapabilityRefusalFrontDoorTextBeforeProvider(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	cmd := &cobra.Command{Use: "show"}
	cmd.Flags().Bool("watch", false, "")
	if err := cmd.Flags().Set("watch", "true"); err != nil {
		t.Fatal(err)
	}
	got := captureStderr(t, func() { _ = validateProxyCapabilitiesBeforeProvider(cmd) })
	if !strings.Contains(got, "watch mode not supported in proxied-server mode") {
		t.Fatalf("text refusal = %q", got)
	}
}

func TestProxyCapabilityRefusalFrontDoorJSONIncludesCode(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	cmd := &cobra.Command{Use: "show"}
	cmd.Flags().Bool("watch", false, "")
	_ = cmd.Flags().Set("watch", "true")
	out := captureStdout(t, func() error {
		_ = validateProxyCapabilitiesBeforeProvider(cmd)
		return nil
	})
	if !strings.Contains(out, `"code": "proxy.watch.unsupported"`) {
		t.Fatalf("JSON refusal = %q", out)
	}
}

func TestProxyCapabilityRefusalDoesNotNeedProvider(t *testing.T) {
	oldProvider := uowProvider
	uowProvider = nil
	t.Cleanup(func() { uowProvider = oldProvider })
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	out := captureStdout(t, func() error {
		_ = runCreateProxiedServer(nil, t.Context(), createInput{repoOverrideSet: true})
		return nil
	})
	if !strings.Contains(out, `"code": "proxy.repo.unsupported"`) {
		t.Fatalf("refusal = %q", out)
	}
}

func TestProxyCapabilityDirectEscapeHatch(t *testing.T) {
	for _, cap := range []ProxyCapability{ProxyCapReadonly, ProxyCapMaxRows, ProxyCapWatch, ProxyCapRepo} {
		if err := AssertProxyCapability(ProxyModeDirect, cap); err != nil {
			t.Errorf("direct %s refused: %v", cap, err)
		}
	}
}
