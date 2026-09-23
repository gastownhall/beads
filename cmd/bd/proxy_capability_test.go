package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestProxyCapabilityMatrix(t *testing.T) {
	for _, capability := range []ProxyCapability{ProxyCapReadonly, ProxyCapMaxRows} {
		err := AssertProxyCapability(ProxyModeProxied, capability)
		if err == nil {
			t.Errorf("%s unexpectedly honored", capability)
		}
		var typed *ProxyCapabilityError
		if !errors.As(err, &typed) || typed.Code == "" || typed.ExitCode != 1 || typed.Mutates {
			t.Errorf("%s error = %#v, want stable non-mutating refusal", capability, err)
		}
	}
	for _, tc := range []struct {
		capability ProxyCapability
		want       string
	}{
		{ProxyCapWatch, "watch mode not supported in proxied-server mode"},
		{ProxyCapRepo, "--repo is not supported with --proxied-server"},
	} {
		err := AssertProxyCapability(ProxyModeProxied, tc.capability)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s error = %v, want %q", tc.capability, err, tc.want)
		}
	}
}

// realCommandPaths walks the actual bd command tree once and returns every
// command path below the root. Policy tables and front-door branches are only
// as good as their correspondence to this set: a key that matches nothing is
// dead policy, and a branch keyed on something that is not a path applies to
// whichever commands happen to share the string.
func realCommandPaths(t *testing.T) map[string]bool {
	t.Helper()
	paths := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if path := commandRegistryPath(c); path != "" {
			paths[path] = true
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	if len(paths) == 0 {
		t.Fatal("walked the real command tree and found no commands")
	}
	return paths
}

func realCommandAtPath(t *testing.T, path string) *cobra.Command {
	t.Helper()
	found, _, err := rootCmd.Find(strings.Split(path, " "))
	if err != nil {
		t.Fatalf("resolve %q in the real command tree: %v", path, err)
	}
	if got := commandRegistryPath(found); got != path {
		t.Fatalf("resolve %q in the real command tree = %q", path, got)
	}
	return found
}

// TestProxyCapabilityPolicyKeysResolveInRealCommandTree is the drift guard for
// the flag-keyed policy table. Every key must name a command that exists, so a
// renamed or removed command turns into a test failure instead of a rule that
// silently stops applying.
func TestProxyCapabilityPolicyKeysResolveInRealCommandTree(t *testing.T) {
	paths := realCommandPaths(t)
	for _, table := range []struct {
		name string
		keys []string
	}{
		// The path-keyed half (proxyCapabilityRegistry) has its own drift guard
		// in TestProxyCapabilityRegistryHasNoStaleRows, so only the flag-keyed
		// table is checked here.
		{"proxyCommandCapabilities", slices.Sorted(maps.Keys(proxyCommandCapabilities))},
	} {
		for _, key := range table.keys {
			if !paths[key] {
				t.Errorf("%s has a row for %q, which is not a real command path", table.name, key)
			}
		}
	}
}

// TestProxyFrontDoorBranchesUseRealCommandPaths pins the command paths the
// three front-door branches key on by hand: the two validators, plus the
// skipsStoreInit route into validateProxyRegistryBeforeProvider in main.go.
// This is the assertion that would have failed on the leaf-name keying: a
// branch written for `bd ready` must name a path, because "ready" as a leaf
// name is also `bd mol ready --gated`.
func TestProxyFrontDoorBranchesUseRealCommandPaths(t *testing.T) {
	paths := realCommandPaths(t)
	for _, path := range []string{"create", "show", "ready", "graph", "find-duplicates", "admin compact", "mol ready", "doctor"} {
		if !paths[path] {
			t.Errorf("front door branches on %q, which is not a real command path", path)
		}
	}
}

// TestProxyCapabilityFrontDoorAllowsSupportedCommands is the allow-path
// complement of the refusal matrix, and it runs against the real command tree
// rather than a synthetic one. The refusal direction degrades to the older,
// opaque late failure when it misses; a spurious refusal breaks a working
// command outright, so the allow path is the side that needs the guard.
func TestProxyCapabilityFrontDoorAllowsSupportedCommands(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, tc := range []struct {
		path       string
		cappedEnv  bool
		wantRefuse bool
	}{
		// `bd mol ready --gated` is the regression: proxy-supported, accepts
		// no --max-rows, and shares its leaf name with `bd ready`.
		{path: "mol ready"},
		{path: "mol ready", cappedEnv: true},
		{path: "ready"},
		{path: "list"},
		{path: "list", cappedEnv: true},
		{path: "dep tree"},
		{path: "dep tree", cappedEnv: true},
		{path: "compact"},
		{path: "compact", cappedEnv: true},
		// `bd ready` is the command the max-rows refusal was written for, so
		// it must still refuse an active cap.
		{path: "ready", cappedEnv: true, wantRefuse: true},
	} {
		name := tc.path
		if tc.cappedEnv {
			name += " with " + maxRowsEnvVar
		}
		t.Run(name, func(t *testing.T) {
			if tc.cappedEnv {
				t.Setenv(maxRowsEnvVar, "5")
			} else {
				t.Setenv(maxRowsEnvVar, "")
			}
			cmd := realCommandAtPath(t, tc.path)
			var err error
			stderr := captureStderr(t, func() {
				err = validateProxyCapabilitiesBeforeProvider(cmd)
				if err == nil {
					// managed-local is the shape that owns its own dolt
					// server, so a refusal here is a policy decision about the
					// command rather than about the deployment.
					err = validateProxyRegistryBeforeProvider(cmd, ProxyTopologyManagedLocal)
				}
			})
			if tc.wantRefuse {
				if err == nil {
					t.Fatalf("bd %s was allowed; want a refusal", tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("bd %s refused before the provider: %v (stderr=%q)", tc.path, err, stderr)
			}
			if stderr != "" {
				t.Fatalf("bd %s wrote %q to stderr on the allow path", tc.path, stderr)
			}
		})
	}
}

// TestProxyCapabilityRulesAlwaysCarryAMessage covers the whole policy surface
// rather than the rows that exist today: a rule that refuses with no message
// renders as a bare "Error: " with exit 1, and an N/A rule describes a command
// that does not have the flag, which is nothing to refuse.
func TestProxyCapabilityRulesAlwaysCarryAMessage(t *testing.T) {
	for _, command := range slices.Sorted(maps.Keys(proxyCommandCapabilities)) {
		for mode, capabilities := range proxyCommandCapabilities[command] {
			for capability, rule := range capabilities {
				err := AssertProxyCommandCapability(command, mode, capability)
				if proxyCapabilityAllowed(rule.Outcome) {
					if err != nil {
						t.Errorf("%s/%s/%s outcome=%s asserted %v, want nil", command, mode, capability, rule.Outcome, err)
					}
					continue
				}
				if err == nil {
					t.Errorf("%s/%s/%s outcome=%s asserted nil, want a refusal", command, mode, capability, rule.Outcome)
					continue
				}
				if err.Error() == "" {
					t.Errorf("%s/%s/%s refused with an empty message", command, mode, capability)
				}
			}
		}
	}
	// A rule with neither a code nor a message must still say something.
	if got := proxyCapabilityRuleError(proxyCapabilityRule{Outcome: ProxyOutcomeRefused}, ProxyCapMaxRows, ProxyModeProxied); got == nil || got.Error() == "" {
		t.Errorf("message-less refusal rendered as %v, want a non-empty error", got)
	}
	// The mirror hole on the same path: the allow branch above returns nil,
	// and the front door wraps that return value, so a rule added as a bare
	// `return HandleProxyCapabilityError(AssertProxy...(...))` must not turn
	// an allowed capability into "Error: <nil>" with exit 1.
	if got := HandleProxyCapabilityError(nil); got != nil {
		t.Errorf("HandleProxyCapabilityError(nil) = %v, want nil", got)
	}
}

// TestListRepoRowMatchesShippedBehavior pins the row against the runtime
// refusal it is supposed to describe. The table documents itself as the
// deterministic audit source of truth, so a row that disagrees with the
// command is worse than an absent one.
func TestListRepoRowMatchesShippedBehavior(t *testing.T) {
	err := AssertProxyCommandCapability("list", ProxyModeProxied, ProxyCapRepo)
	if err == nil {
		t.Fatal("list --repo asserted nil; bd list --repo is refused under --proxied-server")
	}
	if got, want := err.Error(), "--repo is not supported with --proxied-server"; got != want {
		t.Fatalf("list --repo refusal = %q, want %q", got, want)
	}
}

// TestListRepoProxiedRefusalIsStrictJSON pins the refusal's SHAPE, where the
// test above pins only its text. `bd list --repo` is the one --repo refusal
// the pre-provider gate does not shadow (it covers `create` only), so this
// call site is where the contract is actually observed — and a message-only
// assertion is exactly what let the site go untyped while the row still
// promised a code. A wrapper parsing --json must get the stable code and the
// mutates flag on stdout, and the error must still carry exit 1.
func TestListRepoProxiedRefusalIsStrictJSON(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	root := &cobra.Command{Use: "bd"}
	cmd := &cobra.Command{Use: "list"}
	root.AddCommand(cmd)

	var gotErr error
	out := captureStdout(t, func() error {
		gotErr = runListProxiedServer(cmd, context.Background(), io.Discard, listInput{repoOverrideSet: true})
		return nil
	})

	if code, ok := exitCodeFromError(gotErr); !ok || code != 1 {
		t.Fatalf("list --repo refusal exit = %v (typed=%v), want 1", code, ok)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("list --repo refusal is not JSON on stdout: %q (%v)", out, err)
	}
	if got["code"] != "proxy.repo.unsupported" {
		t.Fatalf("list --repo refusal code = %v, want proxy.repo.unsupported (out=%q)", got["code"], out)
	}
	if got["error"] != "--repo is not supported with --proxied-server" {
		t.Fatalf("list --repo refusal error = %v", got["error"])
	}
	if got["mutates"] != false {
		t.Fatalf("list --repo refusal mutates = %v, want false", got["mutates"])
	}
}

// TestReadyClaimResolvesRowCapOnceBeforeProvider pins the malformed-value
// advisory to one line. The command body resolves the cap again for itself, so
// a front door that also warned would print one typo twice.
func TestReadyClaimResolvesRowCapOnceBeforeProvider(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	t.Setenv(maxRowsEnvVar, "bogus")
	root := &cobra.Command{Use: "bd"}
	cmd := &cobra.Command{Use: "ready"}
	cmd.Flags().Bool("claim", false, "")
	cmd.Flags().Int("max-rows", 0, "")
	root.AddCommand(cmd)
	if err := cmd.Flags().Set("claim", "true"); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderr(t, func() { err = validateProxyCapabilitiesBeforeProvider(cmd) })
	if err != nil {
		t.Fatalf("malformed %s refused the claim: %v", maxRowsEnvVar, err)
	}
	if strings.Contains(stderr, "is not a non-negative integer") {
		t.Fatalf("front door warned about %s; the command body is the one that warns (stderr=%q)", maxRowsEnvVar, stderr)
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

// TestProxyMaintenanceNestedPathsRefuseBeforeProvider proves the gate resolves
// a PARENT+CHILD path, which is where a nested command used to slip through.
// It runs on external-tcp because that is the topology on which every path
// listed here still refuses: the backup family is honored on managed-local
// since slice S3, and asserting a refusal there would be asserting the bug.
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
			_ = validateProxyRegistryBeforeProvider(child, ProxyTopologyExternalTCP)
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
		err := validateProxyRegistryBeforeProvider(cmd, ProxyTopologyManagedLocal)
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
			typed := proxyCapabilityErrorFor(row)
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
			out := captureStdout(t, func() error { _ = validateProxyRegistryBeforeProvider(cmd, ProxyTopologyManagedLocal); return nil })
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
	row, ok := LookupCapabilityRow(path, "")
	return row.Rule, ok
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
	err := validateProxyRegistryBeforeProvider(hooks, ProxyTopologyManagedLocal)
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

// newWatchShowCommand builds `bd show --watch`. The root matters: the front
// door keys on a command's path below its root, so a parentless command has no
// path and matches no rule — which is how a test tree can report success for a
// refusal that never fired.
func newWatchShowCommand(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "bd"}
	cmd := &cobra.Command{Use: "show"}
	cmd.Flags().Bool("watch", false, "")
	root.AddCommand(cmd)
	if err := cmd.Flags().Set("watch", "true"); err != nil {
		t.Fatal(err)
	}
	if got := commandRegistryPath(cmd); got != "show" {
		t.Fatalf("test command path = %q, want %q", got, "show")
	}
	return cmd
}

func TestProxyCapabilityRefusalFrontDoorTextBeforeProvider(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	cmd := newWatchShowCommand(t)
	got := captureStderr(t, func() { _ = validateProxyCapabilitiesBeforeProvider(cmd) })
	if !strings.Contains(got, "watch mode not supported in proxied-server mode") {
		t.Fatalf("text refusal = %q", got)
	}
}

func TestProxyCapabilityRefusalFrontDoorJSONIncludesCode(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	cmd := newWatchShowCommand(t)
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

// TestReadyPositiveCapRefusedTypedWithAndWithoutClaim pins one refusal to one
// shape. --claim does not exempt a positive row cap: the proxied ready role
// cannot enforce one on either arm, and ready.go refuses both (see its comment
// above rejectMaxRowsUnderProxiedServer). Exempting the claim at the front door
// would not let it through — it would only downgrade the same refusal to an
// untyped one raised after the provider opened, so `bd ready --max-rows 5` and
// `bd ready --claim --max-rows 5` would answer --json in two different shapes,
// selected by a flag that has nothing to do with the cap.
func TestReadyPositiveCapRefusedTypedWithAndWithoutClaim(t *testing.T) {
	const wantCode = "proxy.max_rows.unsupported"
	const wantMessage = "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode"

	assertTypedRefusal := func(t *testing.T, gotErr error, out string) {
		t.Helper()
		if code, ok := exitCodeFromError(gotErr); !ok || code != 1 {
			t.Fatalf("refusal exit = %v (typed=%v), want 1", code, ok)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("refusal is not JSON on stdout: %q (%v)", out, err)
		}
		if got["code"] != wantCode || got["error"] != wantMessage || got["mutates"] != false {
			t.Fatalf("refusal = %q, want code=%q error=%q mutates=false", out, wantCode, wantMessage)
		}
	}

	for _, tc := range []struct {
		name  string
		claim bool
	}{{name: "bulk"}, {name: "claim", claim: true}} {
		t.Run(tc.name, func(t *testing.T) {
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })

			root := &cobra.Command{Use: "bd"}
			cmd := &cobra.Command{Use: "ready"}
			cmd.Flags().Bool("claim", false, "")
			cmd.Flags().Int("max-rows", 0, "")
			root.AddCommand(cmd)
			if err := cmd.Flags().Set("max-rows", "5"); err != nil {
				t.Fatal(err)
			}
			if tc.claim {
				if err := cmd.Flags().Set("claim", "true"); err != nil {
					t.Fatal(err)
				}
			}

			var gotErr error
			out := captureStdout(t, func() error {
				gotErr = validateProxyCapabilitiesBeforeProvider(cmd)
				return nil
			})
			assertTypedRefusal(t, gotErr, out)
		})
	}

	// The command body refuses the same cap behind the front door. It renders
	// identically so the contract survives on whichever path reaches it first.
	t.Run("backstop", func(t *testing.T) {
		oldJSON := jsonOutput
		jsonOutput = true
		t.Cleanup(func() { jsonOutput = oldJSON })

		var gotErr error
		out := captureStdout(t, func() error {
			gotErr = rejectResolvedMaxRowsUnderProxiedServer(5)
			return nil
		})
		assertTypedRefusal(t, gotErr, out)
	})
}
