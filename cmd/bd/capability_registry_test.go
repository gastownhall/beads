package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cobraBuiltinCommands are added to the tree by cobra itself, not by bd, and
// only once something calls Execute — which another test in this package does
// (completions_test.go runs __complete). Without this set the completeness test
// below would pass or fail depending on test ORDER, which is the worst possible
// property for a policy gate.
var cobraBuiltinCommands = map[string]bool{
	"help": true, "completion": true, "__complete": true, "__completeNoDesc": true,
}

// walkCommandPaths returns every command path in the tree, with the runnable
// ones flagged. Runnable is the property that matters: cobra returns ErrHelp
// for a non-runnable parent before PersistentPreRunE, so a group command never
// reaches the capability gate at all.
func walkCommandPaths(root *cobra.Command) map[string]bool {
	paths := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if cobraBuiltinCommands[c.Name()] {
			return
		}
		if path := commandRegistryPath(c); path != "" {
			paths[path] = c.Runnable()
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return paths
}

// TestProxyCapabilityRegistryCoversCommandTree is the reason this registry
// exists. Every command that can actually run must state what the proxied front
// door does with it. A new command with no row fails here, at build time, rather
// than shipping and dying in the store factory with an untyped error — which is
// how every scattered refusal in this codebase got there.
//
// Adding a row is not a formality: choose the outcome deliberately.
//   - the command works over the proxied provider, or never opens a store at
//     all => add its path to proxyPermittedPaths
//   - the command must not run on a shared/proxied store => add a refusedPath
//     row with Reason=design and say why in a comment
//   - the command could work but nobody has routed it => add a refusedPath row
//     with Reason=unimplemented and a Tracking item
func TestProxyCapabilityRegistryCoversCommandTree(t *testing.T) {
	var missing []string
	for path, runnable := range walkCommandPaths(rootCmd) {
		if !runnable {
			continue
		}
		if _, ok := LookupCapabilityRow(path, ""); !ok {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d command(s) have no proxied-server capability row:\n  %s\n\n"+
			"Add each to cmd/bd/capability_registry.go: proxyPermittedPaths if the proxied "+
			"front door should let it through, or a refusedPath row with a Reason if it should not.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestProxyCapabilityRegistryHasNoStaleRows is the other direction: a row whose
// command no longer exists is policy for nothing, and would hide the renamed
// command's own missing row behind a green test.
func TestProxyCapabilityRegistryHasNoStaleRows(t *testing.T) {
	tree := walkCommandPaths(rootCmd)
	var stale []string
	for _, row := range proxyCapabilityRegistry {
		if _, ok := tree[row.Path]; !ok {
			stale = append(stale, row.display())
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("registry rows for commands that do not exist:\n  %s", strings.Join(stale, "\n  "))
	}
}

func TestProxyCapabilityRegistryHasNoDuplicateRows(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range proxyCapabilityRegistry {
		key := capabilityKey(row.Path, row.ArgSet)
		if seen[key] {
			t.Errorf("duplicate registry row for %q: one of them is dead and the lookup silently picks the last", row.display())
		}
		seen[key] = true
	}
}

// TestProxyCapabilityRegistryRowInvariants pins the contract every row must
// satisfy. The Reason rules are the load-bearing ones: a refusal with no reason
// is exactly the tribal knowledge this registry replaces, and an unimplemented
// refusal with no tracking item is a gap nobody owns.
func TestProxyCapabilityRegistryRowInvariants(t *testing.T) {
	for _, row := range proxyCapabilityRegistry {
		assertCapabilityRuleInvariants(t, row.display(), row.Rule)
		for topology, rule := range row.Topology {
			assertCapabilityRuleInvariants(t, row.display()+" on "+string(topology), rule)
			if topology == ProxyTopologyUnknown {
				t.Errorf("%s: a row must not name the unknown topology; unknown exists so an unclassifiable "+
					"workspace INHERITS the refusal, and exempting it would invert that", row.display())
			}
		}
		if row.History == HistoryDirectOnly && row.Rule.Outcome != ProxyOutcomeRefused {
			t.Errorf("%s: classified direct-only but the gate does not refuse it", row.display())
		}
		if row.History == HistoryProxySupported && row.Rule.Outcome != ProxyOutcomeHonored {
			t.Errorf("%s: classified proxy-supported but the gate refuses it", row.display())
		}
	}
}

// assertCapabilityRuleInvariants checks one rule — a row's default or one of
// its per-topology overrides. Both have to satisfy the same contract: a
// topology override is policy a consumer sees, not a footnote.
func assertCapabilityRuleInvariants(t *testing.T, where string, rule proxyCapabilityRule) {
	t.Helper()
	switch rule.Outcome {
	case ProxyOutcomeRefused:
		if rule.Code == "" || rule.Message == "" || rule.ExitCode != 1 {
			t.Errorf("%s: refusal must carry a code, a message and exit 1, got %#v", where, rule)
		}
		if rule.Mutates {
			t.Errorf("%s: a refusal never mutates", where)
		}
		if !strings.HasPrefix(rule.Code, "proxy.") {
			t.Errorf("%s: code %q must be in the stable proxy.* namespace", where, rule.Code)
		}
	case ProxyOutcomeRefusedInRunE:
		if rule.Code != "" {
			t.Errorf("%s: an untyped RunE refusal has no code; if it has one it belongs in the gate", where)
		}
	case ProxyOutcomeHonored:
		if rule.Reason != "" {
			t.Errorf("%s: Reason explains a refusal; an honored row must not carry one", where)
		}
	default:
		t.Errorf("%s: unexpected outcome %q", where, rule.Outcome)
	}

	if rule.Outcome == ProxyOutcomeRefused || rule.Outcome == ProxyOutcomeRefusedInRunE {
		switch rule.Reason {
		case ProxyReasonDesign, ProxyReasonUnimplemented:
		default:
			t.Errorf("%s: refusal must declare Reason design or unimplemented, got %q", where, rule.Reason)
		}
		if rule.Reason == ProxyReasonUnimplemented && rule.Tracking == "" {
			t.Errorf("%s: an unimplemented refusal needs a Tracking item naming who closes it", where)
		}
	}
}

// inlineProxiedRefusal matches the untyped refusals commands raise from their
// own RunE, e.g. HandleErrorRespectJSON("gitlab sync is not supported in
// proxied-server mode").
var inlineProxiedRefusal = regexp.MustCompile(`HandleErrorRespectJSON\("([^"]*proxied-server mode)"\)`)

// inlineProxiedMessages classifies the messages that do not name their command
// path in the standard "<path> is not supported ..." shape. A path maps the
// message to its registry row; an empty string records a message that mentions
// proxied mode without being a capability refusal at all. Keeping both explicit
// is what lets the scan below be exhaustive rather than best-effort.
var inlineProxiedMessages = map[string]string{
	"only 'compact --dolt' is supported in proxied-server mode": "admin compact",
	// Not a refusal: --database is the flag that only MAKES SENSE on a proxied
	// workspace, so this fires on the direct topologies.
	"--database (or a --db value naming a database) is only supported in proxied-server mode": "",
}

// TestProxyCapabilityRegistryCoversInlineRefusals scans the package for
// refusals raised inside RunE and requires each one to resolve to a registry
// row. Those strings are the untyped half of the refusal surface — no code, no
// mutates flag, nothing a JSON consumer can classify — and before the registry
// they were discoverable only by grep.
//
// The scan runs in both directions on purpose: a new inline refusal must be
// registered, and a ProxyOutcomeRefusedInRunE row whose string has been deleted
// must be flipped to its new outcome by the slice that deleted it.
func TestProxyCapabilityRegistryCoversInlineRefusals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	found := map[string]string{} // registry path -> file it was found in
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range inlineProxiedRefusal.FindAllStringSubmatch(string(src), -1) {
			message := match[1]
			path, ok := inlineRefusalPath(message)
			if !ok {
				t.Errorf("%s: %q mentions proxied-server mode but does not name a command path; "+
					"add it to inlineProxiedMessages so it can be checked against the registry "+
					"(map it to \"\" if it is not a capability refusal)", name, message)
				continue
			}
			if path == "" {
				continue // documented non-refusal
			}
			found[path] = name
		}
	}

	for path, file := range found {
		argSet := ""
		if base, args, split := strings.Cut(path, " --"); split {
			argSet = "--" + args
			path = base
		}
		row, ok := LookupCapabilityRow(path, argSet)
		if !ok {
			t.Errorf("%s refuses %q inside RunE with no registry row; the front door and the command disagree about what bd supports",
				file, strings.TrimSpace(path+" "+argSet))
			continue
		}
		if row.Rule.Outcome == ProxyOutcomeHonored {
			t.Errorf("%s refuses %q inside RunE, but the registry says the front door honors it", file, row.display())
		}
	}

	for _, row := range proxyCapabilityRegistry {
		if row.Rule.Outcome != ProxyOutcomeRefusedInRunE {
			continue
		}
		if _, ok := found[row.Path]; !ok {
			t.Errorf("%s is recorded as an untyped RunE refusal but no such refusal exists in the package any more (%s); "+
				"flip the row to its new outcome", row.display(), row.Note)
		}
	}
}

func inlineRefusalPath(message string) (string, bool) {
	if path, ok := inlineProxiedMessages[message]; ok {
		return path, true
	}
	path, ok := strings.CutSuffix(message, " is not supported in proxied-server mode")
	if !ok {
		return "", false
	}
	// conflicts.go says "bd conflicts ..."; the registry keys on the path.
	return strings.TrimPrefix(path, "bd "), true
}

// TestProxyCapabilityRegistryReasonsAreAssignedIndividually guards the one way
// this registry could become useless while staying green: reasons assigned in
// bulk. Every later slice reads these to decide what is left to do, so both
// values have to be genuinely represented, and the design refusals — the claim
// a reviewer will challenge — are listed here by name.
func TestProxyCapabilityRegistryReasonsAreAssignedIndividually(t *testing.T) {
	wantDesign := map[string]bool{
		"branch": true, "diff": true, "flatten": true,
		"conflicts": true, "conflicts list": true, "conflicts show": true, "conflicts resolve": true,
		"vc": true, "vc merge": true, "vc commit": true, "vc status": true,
		"federation": true, "federation sync": true, "federation status": true,
		"federation add-peer": true, "federation remove-peer": true, "federation list-peers": true,
		"repo": true, "repo add": true, "repo remove": true, "repo list": true, "repo sync": true,
		// `migrate hooks` is deliberately NOT here: it migrates git hook files
		// and opens no store, so the surgery rationale the rest of this block
		// rests on does not describe it. Its refusal is structural — the
		// command is on none of main.go's store-init skip lists — which makes
		// it unimplemented with a tracking item, not design.
		"migrate": true, "migrate sync": true, "migrate issues": true,
		"migrate-issues": true, "migrate-personal": true,
		"admin cleanup": true, "admin reset": true,
		// The backup family's DEFAULT rule is the design refusal; managed-local
		// overrides it to honored (TestProxiedBackupHonoredOnlyOnManagedLocal).
		// It is design because a backup remote is registered on the SERVER,
		// where it is global to every client of that server — true of every
		// destination scheme, unlike the filesystem half, which is a file://
		// property only. These rows carry a Tracking item anyway
		// (backupRemoteSchemeTracking): the outcome is settled, the
		// remote-scheme case is an open question and must not read as closed.
		"backup": true, "backup init": true, "backup sync": true,
		"backup remove": true, "backup status": true, "backup restore": true,
	}

	gotDesign := map[string]bool{}
	unimplemented := 0
	for _, row := range proxyCapabilityRegistry {
		switch row.Rule.Reason {
		case ProxyReasonDesign:
			gotDesign[row.display()] = true
		case ProxyReasonUnimplemented:
			unimplemented++
		}
	}

	for path := range wantDesign {
		if !gotDesign[path] {
			t.Errorf("%q is no longer refused by design; if that is intended, update this test and say why in the PR", path)
		}
	}
	for path := range gotDesign {
		if !wantDesign[path] {
			t.Errorf("%q claims Reason=design and is not in the reviewed list; a design refusal is a semantic claim that needs review", path)
		}
	}
	if unimplemented == 0 {
		t.Error("no unimplemented refusals left — either the work is done or the reasons were bulk-assigned")
	}
}

// TestProxyCapabilityArgSetFallsBackToBarePath pins the lookup rule that lets
// one table carry both the path policy and the flag policy: a flag combination
// with no row of its own inherits the bare path's verdict rather than escaping
// it. Written against the real command tree so it exercises the flags these
// commands actually declare.
func TestProxyCapabilityArgSetFallsBackToBarePath(t *testing.T) {
	for _, tc := range []struct {
		path    string
		flags   []string
		want    ProxyCapabilityOutcome
		wantMsg string
	}{
		// Covered by an explicit row: the message names the flags.
		{"rename-prefix", []string{"dry-run"}, ProxyOutcomeRefused, "rename-prefix --dry-run is not supported in proxied-server mode"},
		// No row for this combination: it inherits the refused bare row rather
		// than slipping past the gate.
		{"rename-prefix", []string{"dry-run", "auto-merge"}, ProxyOutcomeRefused, "rename-prefix is not supported in proxied-server mode"},
		// Inheriting an honored bare row is equally load-bearing: this is the
		// read-only preview and it must keep working.
		{"duplicates", []string{"dry-run"}, ProxyOutcomeHonored, ""},
	} {
		t.Run(tc.path+" "+strings.Join(tc.flags, " "), func(t *testing.T) {
			root := &cobra.Command{Use: "bd"}
			cmd := &cobra.Command{Use: tc.path}
			root.AddCommand(cmd)
			for _, flag := range tc.flags {
				cmd.Flags().Bool(flag, false, "")
				if err := cmd.Flags().Set(flag, "true"); err != nil {
					t.Fatal(err)
				}
			}
			row, ok := capabilityRowFor(cmd)
			if !ok {
				t.Fatalf("no row resolved for %q with %v", tc.path, tc.flags)
			}
			if row.Rule.Outcome != tc.want || row.Rule.Message != tc.wantMsg {
				t.Fatalf("row = outcome %q message %q, want %q / %q",
					row.Rule.Outcome, row.Rule.Message, tc.want, tc.wantMsg)
			}
		})
	}
}

// TestProxyRefusalJSONCarriesReason pins the additive half of the JSON
// contract. {code, error, mutates} is frozen and parsed downstream; reason is
// new, and it must appear without disturbing the three fields that were already
// there.
func TestProxyRefusalJSONCarriesReason(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	for _, tc := range []struct {
		path, code string
		reason     ProxyRefusalReason
	}{
		{"branch", "proxy.branch.unsupported", ProxyReasonDesign},
		{"sync", "proxy.sync.unsupported", ProxyReasonUnimplemented},
	} {
		t.Run(tc.path, func(t *testing.T) {
			root := &cobra.Command{Use: "bd"}
			cmd := &cobra.Command{Use: tc.path}
			root.AddCommand(cmd)
			out := captureStdout(t, func() error {
				_ = validateProxyRegistryBeforeProvider(cmd, ProxyTopologyManagedLocal)
				return nil
			})
			for _, want := range []string{
				`"code": "` + tc.code + `"`,
				`"error": "` + tc.path + ` is not supported in proxied-server mode"`,
				`"mutates": false`,
				`"reason": "` + string(tc.reason) + `"`,
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("refusal JSON missing %s:\n%s", want, out)
				}
			}
		})
	}
}

// TestProxiedStoreUnroutedIsTyped covers the backstop: a permitted command with
// no proxied route used to die with a bare string that no consumer could tell
// from a real store failure.
func TestProxiedStoreUnroutedIsTyped(t *testing.T) {
	var typed *ProxyCapabilityError
	err := errProxiedStoreUnrouted()
	if !errors.As(err, &typed) {
		t.Fatalf("store backstop error = %#v, want *ProxyCapabilityError", err)
	}
	if typed.Code != "proxy.store.unrouted" || typed.ExitCode != 1 || typed.Mutates {
		t.Fatalf("store backstop = %#v", typed)
	}
	if typed.Reason != ProxyReasonUnimplemented {
		t.Fatalf("store backstop reason = %q, want unimplemented", typed.Reason)
	}
}
