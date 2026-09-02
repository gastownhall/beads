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

func transformTestCommand(path string, flags ...string) *cobra.Command {
	root := &cobra.Command{Use: "bd"}
	cmd := &cobra.Command{Use: path}
	for _, flag := range flags {
		cmd.Flags().Bool(flag, false, "")
	}
	root.AddCommand(cmd)
	return cmd
}

func TestProxyTransformCapabilityRows(t *testing.T) {
	cases := []struct {
		name, path, argument string
		flags                []string
		outcome              ProxyCapabilityOutcome
		code, message        string
		exit                 int
		mutates              bool
	}{
		{name: "rename", path: "rename", outcome: ProxyOutcomeRefused, code: "proxy.transform.unsupported", message: "rename is not supported in proxied-server mode", exit: 1},
		{name: "rename-prefix", path: "rename-prefix", outcome: ProxyOutcomeRefused, code: "proxy.transform.unsupported", message: "rename-prefix is not supported in proxied-server mode", exit: 1},
		{name: "duplicate", path: "duplicate", outcome: ProxyOutcomeRefused, code: "proxy.transform.unsupported", message: "duplicate is not supported in proxied-server mode", exit: 1},
		{name: "supersede", path: "supersede", outcome: ProxyOutcomeRefused, code: "proxy.transform.unsupported", message: "supersede is not supported in proxied-server mode", exit: 1},
		{name: "duplicates", path: "duplicates", flags: []string{"auto-merge", "dry-run"}, outcome: ProxyOutcomeHonored},
		{name: "duplicates auto-merge", path: "duplicates", argument: "--auto-merge", flags: []string{"auto-merge", "dry-run"}, outcome: ProxyOutcomeRefused, code: "proxy.transform.unsupported", message: "duplicates --auto-merge is not supported in proxied-server mode", exit: 1},
		{name: "duplicates auto-merge dry-run", path: "duplicates", argument: "--auto-merge --dry-run", flags: []string{"auto-merge", "dry-run"}, outcome: ProxyOutcomeHonored},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := transformTestCommand(tc.path, tc.flags...)
			if tc.argument != "" {
				for _, flag := range strings.Fields(tc.argument) {
					if err := cmd.Flags().Set(strings.TrimPrefix(flag, "--"), "true"); err != nil {
						t.Fatal(err)
					}
				}
			}
			row, ok := lookupTransformCapability(cmd)
			if !ok {
				t.Fatalf("lookup(%q, %q) missing row", tc.path, tc.argument)
			}
			if row.Path != tc.path || row.Argument != tc.argument || row.Outcome != tc.outcome {
				t.Fatalf("row = %#v, want path=%q argument=%q outcome=%q", row, tc.path, tc.argument, tc.outcome)
			}
			if tc.outcome == ProxyOutcomeRefused {
				if row.Code != tc.code || row.Message != tc.message || row.ExitCode != tc.exit || row.Mutates != tc.mutates {
					t.Fatalf("refusal fields = code=%q message=%q exit=%d mutates=%t", row.Code, row.Message, row.ExitCode, row.Mutates)
				}
				typed := transformCapabilityError(row)
				var got *ProxyCapabilityError
				if !errors.As(typed, &got) || got.Code != tc.code || got.ExitCode != 1 || got.Mutates {
					t.Fatalf("typed refusal = %#v", typed)
				}
			}
			if err := validateProxyTransformBeforeProvider(cmd); tc.outcome == ProxyOutcomeHonored && err != nil {
				t.Fatalf("honored transform refused: %v", err)
			}
		})
	}
}

func TestProxyDuplicatesAutoMergeDryRunMatrix(t *testing.T) {
	cmd := transformTestCommand("duplicates", "auto-merge", "dry-run")
	_ = cmd.Flags().Set("auto-merge", "true")
	if err := validateProxyTransformBeforeProvider(cmd); err == nil {
		t.Fatalf("non-dry auto-merge error = %v", err)
	}
	_ = cmd.Flags().Set("dry-run", "true")
	if err := validateProxyTransformBeforeProvider(cmd); err != nil {
		t.Fatalf("dry-run auto-merge refused: %v", err)
	}
}

func TestProxyTransformRefusalRendering(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "")
	cmd := transformTestCommand("rename")
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })

	jsonOutput = false
	var textErr string
	stderr := captureStderr(t, func() {
		err := validateProxyTransformBeforeProvider(cmd)
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Fatalf("exit = %v, want 1", err)
		}
		textErr = err.Error()
	})
	if textErr != "exit code 1" || stderr != "Error: rename is not supported in proxied-server mode\n" {
		t.Fatalf("text refusal = %q (err %q)", stderr, textErr)
	}

	jsonOutput = true
	stdout := captureStdout(t, func() error {
		_ = validateProxyTransformBeforeProvider(cmd)
		return nil
	})
	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("JSON refusal: %v (%q)", err, stdout)
	}
	if envelope["code"] != "proxy.transform.unsupported" || envelope["error"] != "rename is not supported in proxied-server mode" {
		t.Fatalf("JSON refusal = %#v", envelope)
	}
}

func TestProxyTransformRefusalHasNoProviderOrFileMutation(t *testing.T) {
	dir := t.TempDir()
	version := []byte("version-before\n")
	issues := []byte(`{"id":"before","title":"untouched"}` + "\n")
	versionPath := dir + "/.local_version"
	issuesPath := dir + "/issues.jsonl"
	if err := os.WriteFile(versionPath, version, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(issuesPath, issues, 0o600); err != nil {
		t.Fatal(err)
	}
	oldProvider := uowProvider
	uowProvider = nil
	t.Cleanup(func() { uowProvider = oldProvider })

	cmd := transformTestCommand("supersede")
	if err := validateProxyTransformBeforeProvider(cmd); err == nil {
		t.Fatal("expected transform refusal")
	}
	if uowProvider != nil {
		t.Fatal("transform refusal initialized a provider")
	}
	gotVersion, err := os.ReadFile(versionPath)
	if err != nil {
		t.Fatal(err)
	}
	gotIssues, err := os.ReadFile(issuesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotVersion, version) || !bytes.Equal(gotIssues, issues) {
		t.Fatal("transform refusal mutated workspace files")
	}
}
