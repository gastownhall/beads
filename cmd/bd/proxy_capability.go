package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ProxyCapability identifies a command feature whose availability differs by
// storage topology.
type ProxyCapability string

const (
	ProxyCapReadonly ProxyCapability = "readonly"
	ProxyCapMaxRows  ProxyCapability = "max-rows"
	ProxyCapWatch    ProxyCapability = "watch"
	ProxyCapRepo     ProxyCapability = "repo"
)

// ProxyMode identifies the selected storage topology.
type ProxyMode string

const (
	ProxyModeDirect  ProxyMode = "direct"
	ProxyModeProxied ProxyMode = "proxied-server"
)

// ProxyTopology distinguishes the provider deployment shape. The backup family
// is the first policy that genuinely differs across these: a Dolt backup
// destination is resolved on the machine running dolt, so "the server bd
// spawned itself" and "a server somebody else runs" are different answers to
// the same command (see capability_registry.go's backup rows).
type ProxyTopology string

const (
	ProxyTopologyAny          ProxyTopology = "any"
	ProxyTopologyManagedLocal ProxyTopology = "managed-local"
	ProxyTopologyExternalTCP  ProxyTopology = "external-tcp"
	ProxyTopologyExternalUnix ProxyTopology = "external-unix"
	// ProxyTopologyTeamServer is a workspace whose database is owned by
	// beads-team-server. It is a shape rather than a transport: bts owns the
	// store's schema and identity whether bd reaches it over a port, a socket,
	// or a child bd started, so the ownership fact decides capability policy and
	// the transport does not.
	ProxyTopologyTeamServer ProxyTopology = "team-server"
	// ProxyTopologyUnknown is what a workspace bd cannot classify reports. It is
	// never honored by a topology-keyed row, which is the fail-closed half of
	// the policy: a workspace that cannot prove it owns its Dolt server does not
	// get the privileges of one that can.
	ProxyTopologyUnknown ProxyTopology = "unknown"
)

// ProxyCapabilityOutcome describes what the front door does with a feature.
type ProxyCapabilityOutcome string

const (
	ProxyOutcomeHonored   ProxyCapabilityOutcome = "honored"
	ProxyOutcomeRefused   ProxyCapabilityOutcome = "refused"
	ProxyOutcomeDelegated ProxyCapabilityOutcome = "delegated"
	ProxyOutcomeNA        ProxyCapabilityOutcome = "N/A"
	// ProxyOutcomeRefusedInRunE records a path the gate permits and that the
	// command then refuses itself, untyped, from RunE. It is inventory, not
	// policy: nothing enforces it. See capability_registry.go.
	ProxyOutcomeRefusedInRunE ProxyCapabilityOutcome = "refused-in-run"
)

// proxyCapabilityRule is the stable contract for one command/argument/topology
// capability. Mutates is false for all refusals; ExitCode is used by the CLI
// when rendering a typed refusal. Reason and Tracking say why a refusal exists
// and who closes it — see the header of capability_registry.go.
type proxyCapabilityRule struct {
	Outcome  ProxyCapabilityOutcome
	Code     string
	Message  string
	ExitCode int
	Mutates  bool
	Reason   ProxyRefusalReason
	Tracking string
}

// ProxyCapabilityError is a machine-identifiable front-door refusal.
type ProxyCapabilityError struct {
	Code     string
	Message  string
	ExitCode int
	Mutates  bool
	Reason   ProxyRefusalReason
}

func (e *ProxyCapabilityError) Error() string { return e.Message }

// errProxiedStoreUnrouted is the backstop for a command that the front door
// permitted and that then asked for a classic store on a proxied-server
// workspace. The proxied topology has no such store — the provider owns the
// connection — so the factory refuses rather than opening the proxied root a
// second time behind the proxy's back, which is the one failure mode every
// slice of this work must not regress.
//
// Reaching this is a routing gap in the command, not a policy decision: the
// registry says the gate permits the path, and permitting a path is not a
// promise that a proxied route exists for it. The owner is therefore whichever
// slice routes the command that landed here, which is why the reason is
// "unimplemented" and no single tracking item fits. It is typed so that a
// caller can classify it; before the registry there was no way to tell this
// apart from a genuine store failure.
func errProxiedStoreUnrouted() error {
	return &ProxyCapabilityError{
		Code:     "proxy.store.unrouted",
		Message:  "this command has no proxied-server route; bd will not open a proxied-server workspace as a direct store",
		ExitCode: 1,
		Reason:   ProxyReasonUnimplemented,
	}
}

func refused(code, message string, reason ProxyRefusalReason, tracking string) proxyCapabilityRule {
	return proxyCapabilityRule{
		Outcome: ProxyOutcomeRefused, Code: code, Message: message, ExitCode: 1,
		Reason: reason, Tracking: tracking,
	}
}

func honored() proxyCapabilityRule {
	return proxyCapabilityRule{Outcome: ProxyOutcomeHonored, ExitCode: 0}
}

func notApplicable() proxyCapabilityRule { return proxyCapabilityRule{Outcome: ProxyOutcomeNA} }

// ProxyCapabilityKey identifies a command's flag/argument on a topology.
// Command is the command's path below the root ("dep tree", "mol ready"),
// never its cobra leaf name — see commandRegistryPath. Argument is intentionally
// explicit (for example, "--watch"), allowing callers and tests to
// distinguish a command that lacks a flag (N/A) from one that refuses it.
type ProxyCapabilityKey struct {
	Command  string
	Argument string
	Mode     ProxyMode
	Topology ProxyTopology
}

// ProxyCapabilityRow is an inspectable command/argument/topology policy row.
type ProxyCapabilityRow struct {
	ProxyCapabilityKey
	Rule proxyCapabilityRule
}

// LookupProxyCapabilityFor returns the command/argument-specific rule. An
// absent command row falls back to the topology-wide default.
func LookupProxyCapabilityFor(command, argument string, mode ProxyMode) (proxyCapabilityRule, bool) {
	capability := ProxyCapability(argument)
	if len(argument) > 2 && argument[:2] == "--" {
		capability = ProxyCapability(argument[2:])
	}
	if commands, ok := proxyCommandCapabilities[command]; ok {
		if modes, ok := commands[mode]; ok {
			if rule, ok := modes[capability]; ok {
				return rule, true
			}
		}
	}
	return LookupProxyCapability(mode, capability)
}

// Shared by the mode-wide rule and its per-command overrides, so a command that
// refuses one of these refuses it with the same words and the same reason.
var (
	// The cap is refused rather than silently dropped, which is right; the fix
	// is threading it through the UOW reader the way `list` already does, so
	// the refusal itself is the gap.
	maxRowsRefusal = refused("proxy.max_rows.unsupported", "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode", ProxyReasonUnimplemented, trackLongTail)
	// `list --watch` is routed, so watching is possible over the provider;
	// nothing else has been wired to it.
	watchRefusal = refused("proxy.watch.unsupported", "watch mode not supported in proxied-server mode", ProxyReasonUnimplemented, trackLongTail)
	// --repo routes to another workspace, bypassing the proxied root entirely,
	// which is why it is design rather than a gap. `bd create` is the only
	// command that registers the flag, so it is the only command this rule can
	// actually refuse; every other row for it must be notApplicable(), which is
	// what TestProxyCapabilityRowsNameFlagsTheCommandRegisters enforces.
	repoRefusal = refused("proxy.repo.unsupported", "--repo is not supported with --proxied-server", ProxyReasonDesign, "")
)

// proxyCapabilityMatrix is the flag-keyed half of the policy; the path-keyed
// half is the registry in capability_registry.go. Reasons follow the same rule
// there and here.
//
// Strict --readonly is the one capability that needs real backend work rather
// than routing: it is a GUARANTEE, the proxied provider carries no read-only
// posture, and the only honest implementation is a read-only SQL principal on
// the backend — which on external topologies is the operator's configuration,
// not bd's. A best-effort version would betray exactly the workflows that ask
// for it, so it is refused by design until that principal exists. --repo is
// design for a simpler reason: it routes to another workspace, bypassing the
// proxied root entirely.
var proxyCapabilityMatrix = map[ProxyMode]map[ProxyCapability]proxyCapabilityRule{
	ProxyModeDirect: {
		ProxyCapReadonly: honored(), ProxyCapMaxRows: honored(),
		ProxyCapWatch: honored(), ProxyCapRepo: honored(),
	},
	ProxyModeProxied: {
		ProxyCapReadonly: refused("proxy.readonly.unsupported", "strict readonly is unavailable for dolt proxied-server backend; refusing to open a store that cannot guarantee mutation-free access", ProxyReasonDesign, "1.4 candidate: read-only SQL principal on the backend"),
		ProxyCapMaxRows:  maxRowsRefusal,
		ProxyCapWatch:    watchRefusal,
		ProxyCapRepo:     repoRefusal,
	},
}

// proxyCommandCapabilities overrides the mode-wide default for one command.
// Keys are command paths (commandRegistryPath), so a row lands on the command it
// was written for and on nothing else. The "mol ready" row is a documentation
// pin rather than the guard: path keying is what stops `bd mol ready --gated`
// from inheriting the refusal `bd ready` carries on their shared leaf name
// "ready", and the row records that the command has no --max-rows flag to
// refuse. TestProxyCapabilityPolicyKeysResolveInRealCommandTree keeps the pin
// honest by failing if the path it names stops existing.
//
// A row must describe a flag the command actually registers. `list`'s
// ProxyCapRepo is notApplicable() for that reason and not as a hedge: listCmd
// has no --repo flag (create.go:940 registers the only one), so `bd list --repo`
// dies in cobra as an unknown flag and a refusal row here would be policy no
// command line can reach. TestProxyCapabilityRowsNameFlagsTheCommandRegisters
// enforces both directions of that rule.
var proxyCommandCapabilities = map[string]map[ProxyMode]map[ProxyCapability]proxyCapabilityRule{
	"show":            {ProxyModeProxied: {ProxyCapWatch: watchRefusal}},
	"list":            {ProxyModeProxied: {ProxyCapWatch: honored(), ProxyCapMaxRows: honored(), ProxyCapRepo: notApplicable()}},
	"dep tree":        {ProxyModeProxied: {ProxyCapMaxRows: honored()}},
	"ready":           {ProxyModeProxied: {ProxyCapMaxRows: maxRowsRefusal}},
	"mol ready":       {ProxyModeProxied: {ProxyCapMaxRows: notApplicable()}},
	"graph":           {ProxyModeProxied: {ProxyCapMaxRows: maxRowsRefusal}},
	"find-duplicates": {ProxyModeProxied: {ProxyCapMaxRows: maxRowsRefusal}},
}

// proxyCapabilityRows materializes the policy for every supported proxied
// topology. Keeping rows explicit makes matrix audits and front-door tests
// deterministic even though the current provider implementations share rules.
var proxyCapabilityRows = buildProxyCapabilityRows()

func buildProxyCapabilityRows() []ProxyCapabilityRow {
	topologies := []ProxyTopology{ProxyTopologyManagedLocal, ProxyTopologyExternalTCP, ProxyTopologyExternalUnix}
	var rows []ProxyCapabilityRow
	for _, topology := range topologies {
		for _, capability := range []ProxyCapability{ProxyCapReadonly, ProxyCapMaxRows, ProxyCapWatch, ProxyCapRepo} {
			rule, _ := LookupProxyCapability(ProxyModeProxied, capability)
			rows = append(rows, ProxyCapabilityRow{ProxyCapabilityKey{"", string(capability), ProxyModeProxied, topology}, rule})
		}
		for command, modes := range proxyCommandCapabilities {
			for mode, capabilities := range modes {
				for capability, rule := range capabilities {
					rows = append(rows, ProxyCapabilityRow{ProxyCapabilityKey{command, string(capability), mode, topology}, rule})
				}
			}
		}
	}
	return rows
}

// LookupProxyCapabilityAt returns a topology-keyed rule. Unknown topology is
// rejected; ProxyTopologyAny applies the topology-independent direct policy.
func LookupProxyCapabilityAt(command, argument string, mode ProxyMode, topology ProxyTopology) (proxyCapabilityRule, bool) {
	if topology != ProxyTopologyAny && topology != ProxyTopologyManagedLocal && topology != ProxyTopologyExternalTCP && topology != ProxyTopologyExternalUnix {
		return proxyCapabilityRule{}, false
	}
	if len(argument) > 2 && argument[:2] == "--" {
		argument = argument[2:]
	}
	for _, row := range proxyCapabilityRows {
		if row.Command == command && row.Argument == argument && row.Mode == mode && row.Topology == topology {
			return row.Rule, true
		}
	}
	if topology == ProxyTopologyAny {
		return LookupProxyCapabilityFor(command, argument, mode)
	}
	return proxyCapabilityRule{}, false
}

// LookupProxyCapability returns the typed rule for a mode/capability pair.
func LookupProxyCapability(mode ProxyMode, capability ProxyCapability) (proxyCapabilityRule, bool) {
	rules, ok := proxyCapabilityMatrix[mode]
	if !ok {
		return proxyCapabilityRule{}, false
	}
	rule, ok := rules[capability]
	return rule, ok
}

// AssertProxyCapability rejects unsupported features before provider setup.
func AssertProxyCapability(mode ProxyMode, capability ProxyCapability) error {
	return AssertProxyCommandCapability("", mode, capability)
}

// proxyCapabilityAllowed reports whether an outcome lets the invocation
// proceed. ProxyOutcomeNA means the command does not have the flag at all
// (see ProxyCapabilityKey), which is a fact about the command rather than a
// policy objection: there is nothing to refuse, so it asserts successfully.
func proxyCapabilityAllowed(outcome ProxyCapabilityOutcome) bool {
	return outcome == ProxyOutcomeHonored || outcome == ProxyOutcomeDelegated || outcome == ProxyOutcomeNA
}

// proxyCapabilityRuleError renders a non-allowing rule as an error. A rule
// with neither a code nor a message would otherwise produce an error whose
// Error() is the empty string, which the CLI prints as a bare "Error: " with
// exit 1 — the least debuggable failure available — so it falls back to a
// generic message no matter how a future rule is written.
// It builds the typed error through proxyCapabilityErrorFor so a rule's Reason
// reaches the JSON refusal here exactly as it does from the registry half; a
// local struct literal would silently drop it.
func proxyCapabilityRuleError(rule proxyCapabilityRule, capability ProxyCapability, mode ProxyMode) error {
	if rule.Code != "" {
		if rule.Message == "" {
			// rule is a copy, so this fills the fallback in for this render
			// only and never edits the policy table.
			rule.Message = fmt.Sprintf("%s is not supported in %s mode", capability, mode)
		}
		return proxyCapabilityErrorFor(rule)
	}
	if rule.Message != "" {
		return fmt.Errorf("%s", rule.Message)
	}
	return fmt.Errorf("%s is not supported in %s mode", capability, mode)
}

// AssertProxyCommandCapability checks a command-specific capability rule.
// command is a command path (commandRegistryPath), not a cobra leaf name.
func AssertProxyCommandCapability(command string, mode ProxyMode, capability ProxyCapability) error {
	if commands, ok := proxyCommandCapabilities[command]; ok {
		if modes, ok := commands[mode]; ok {
			if rule, ok := modes[capability]; ok {
				if proxyCapabilityAllowed(rule.Outcome) {
					return nil
				}
				return proxyCapabilityRuleError(rule, capability, mode)
			}
		}
	}
	rule, ok := LookupProxyCapability(mode, capability)
	if !ok || !proxyCapabilityAllowed(rule.Outcome) {
		return proxyCapabilityRuleError(rule, capability, mode)
	}
	return nil
}

// validateProxyCapabilitiesBeforeProvider runs refusals that can be decided
// from argv before the proxied provider is opened.
func validateProxyCapabilitiesBeforeProvider(cmd *cobra.Command) error {
	if cmd == nil {
		return nil
	}
	path := commandRegistryPath(cmd)
	if path == "create" && cmd.Flags().Changed("repo") {
		return HandleProxyCapabilityError(AssertProxyCapability(ProxyModeProxied, ProxyCapRepo))
	}
	if path == "show" {
		if watch, _ := cmd.Flags().GetBool("watch"); watch {
			return HandleProxyCapabilityError(AssertProxyCommandCapability(path, ProxyModeProxied, ProxyCapWatch))
		}
	}
	if path == "ready" && !readyGatedArm(cmd) {
		// --claim is NOT exempt. The proxied ready role cannot enforce a row
		// cap on either arm, and ready.go refuses a positive cap on both (see
		// its comment above rejectMaxRowsUnderProxiedServer) — so exempting
		// the claim here would not have let it through, only downgraded the
		// same refusal to an untyped one raised after the provider opened.
		// A malformed or negative value is still rejected here, before any
		// provider work, which is what the claim path gained.
		//
		// --gated IS exempt, and the guard sits on the branch rather than
		// inside it so the arm skips the resolver too: the direct route never
		// resolves a cap before dispatching --gated, so resolving one here
		// would make a malformed or negative value fail on the proxied route
		// alone. See readyGatedArm for why the arm takes no cap at all.
		maxRows, err := resolveMaxRowsQuiet(cmd)
		if err != nil {
			return err
		}
		if maxRows > 0 {
			return HandleProxyCapabilityError(AssertProxyCommandCapability(path, ProxyModeProxied, ProxyCapMaxRows))
		}
	}
	if path == "graph" || path == "find-duplicates" {
		maxRows, err := resolveMaxRowsQuiet(cmd)
		if err != nil {
			return err
		}
		if maxRows > 0 {
			return HandleProxyCapabilityError(AssertProxyCommandCapability(path, ProxyModeProxied, ProxyCapMaxRows))
		}
	}
	return nil
}
