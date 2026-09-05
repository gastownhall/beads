package main

import (
	"errors"
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

func TestProxyCapabilityDirectEscapeHatch(t *testing.T) {
	for _, cap := range []ProxyCapability{ProxyCapReadonly, ProxyCapMaxRows, ProxyCapWatch, ProxyCapRepo} {
		if err := AssertProxyCapability(ProxyModeDirect, cap); err != nil {
			t.Errorf("direct %s refused: %v", cap, err)
		}
	}
}

func TestReadyClaimValidatesMaxRowsBeforeProvider(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	root := &cobra.Command{Use: "bd"}
	cmd := &cobra.Command{Use: "ready"}
	cmd.Flags().Bool("claim", false, "")
	cmd.Flags().Int("max-rows", 0, "")
	root.AddCommand(cmd)
	if err := cmd.Flags().Set("claim", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("max-rows", "-1"); err != nil {
		t.Fatal(err)
	}
	var gotErr error
	out := captureStdout(t, func() error {
		gotErr = validateProxyCapabilitiesBeforeProvider(cmd)
		return nil
	})
	if code, ok := exitCodeFromError(gotErr); !ok || code != 1 {
		t.Fatalf("ready claim invalid max-rows exit = %v, want 1", code)
	}
	if !strings.Contains(out, "--max-rows must be non-negative") {
		t.Fatalf("ready claim invalid max-rows refusal = %q", out)
	}
}
