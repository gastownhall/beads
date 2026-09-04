package main

import (
	"fmt"
	"strings"

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

// ProxyTopology distinguishes the provider deployment shape. Capability
// policy is currently identical across proxied shapes, but retaining this
// dimension prevents an external TCP server from being conflated with a local
// managed one as more surfaces are added.
type ProxyTopology string

const (
	ProxyTopologyAny          ProxyTopology = "any"
	ProxyTopologyManagedLocal ProxyTopology = "managed-local"
	ProxyTopologyExternalTCP  ProxyTopology = "external-tcp"
	ProxyTopologyExternalUnix ProxyTopology = "external-unix"
)

// ProxyCapabilityOutcome describes what the front door does with a feature.
type ProxyCapabilityOutcome string

const (
	ProxyOutcomeHonored   ProxyCapabilityOutcome = "honored"
	ProxyOutcomeRefused   ProxyCapabilityOutcome = "refused"
	ProxyOutcomeDelegated ProxyCapabilityOutcome = "delegated"
	ProxyOutcomeNA        ProxyCapabilityOutcome = "N/A"
)

// proxyCapabilityRule is the stable contract for one command/argument/topology
// capability. Mutates is false for all refusals; ExitCode is used by the CLI
// when rendering a typed refusal.
type proxyCapabilityRule struct {
	Outcome  ProxyCapabilityOutcome
	Code     string
	Message  string
	ExitCode int
	Mutates  bool
}

// ProxyCapabilityError is a machine-identifiable front-door refusal.
type ProxyCapabilityError struct {
	Code     string
	Message  string
	ExitCode int
	Mutates  bool
}

func (e *ProxyCapabilityError) Error() string { return e.Message }

func refused(code, message string) proxyCapabilityRule {
	return proxyCapabilityRule{Outcome: ProxyOutcomeRefused, Code: code, Message: message, ExitCode: 1}
}

func honored() proxyCapabilityRule {
	return proxyCapabilityRule{Outcome: ProxyOutcomeHonored, ExitCode: 0}
}

func notApplicable() proxyCapabilityRule { return proxyCapabilityRule{Outcome: ProxyOutcomeNA} }

// ProxyCapabilityKey identifies a command's flag/argument on a topology.
// Argument is intentionally explicit (for example, "--watch"), allowing
// callers and tests to distinguish a command that lacks a flag (N/A) from one
// that refuses it.
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

var proxyMaintenanceRefusals = map[string]proxyCapabilityRule{
	"doctor":           refused("proxy.doctor.unsupported", "doctor is not supported in proxied-server mode"),
	"backup":           refused("proxy.backup.unsupported", "backup is not supported in proxied-server mode"),
	"restore":          refused("proxy.restore.unsupported", "restore is not supported in proxied-server mode"),
	"diff":             refused("proxy.diff.unsupported", "diff is not supported in proxied-server mode"),
	"flatten":          refused("proxy.flatten.unsupported", "flatten is not supported in proxied-server mode"),
	"migrate":          refused("proxy.migrate.unsupported", "migrate is not supported in proxied-server mode"),
	"migrate-personal": refused("proxy.migrate.unsupported", "migrate-personal is not supported in proxied-server mode"),
	"branch":           refused("proxy.branch.unsupported", "branch is not supported in proxied-server mode"),
	"conflicts":        refused("proxy.conflicts.unsupported", "conflicts is not supported in proxied-server mode"),
	"vc":               refused("proxy.vc.unsupported", "vc is not supported in proxied-server mode"),
	"federation":       refused("proxy.federation.unsupported", "federation is not supported in proxied-server mode"),
	"repo":             refused("proxy.repo.unsupported", "repo is not supported in proxied-server mode"),
	// Wording matches the long-standing compact.go refusal this pre-provider
	// gate now short-circuits, so the user-facing message does not change.
	"compact":                refused("proxy.compact.unsupported", "only 'compact --dolt' is supported in proxied-server mode"),
	"backup init":            refused("proxy.backup.unsupported", "backup init is not supported in proxied-server mode"),
	"backup sync":            refused("proxy.backup.unsupported", "backup sync is not supported in proxied-server mode"),
	"backup remove":          refused("proxy.backup.unsupported", "backup remove is not supported in proxied-server mode"),
	"backup status":          refused("proxy.backup.unsupported", "backup status is not supported in proxied-server mode"),
	"backup restore":         refused("proxy.backup.unsupported", "backup restore is not supported in proxied-server mode"),
	"migrate sync":           refused("proxy.migrate.unsupported", "migrate sync is not supported in proxied-server mode"),
	"migrate-issues":         refused("proxy.migrate.unsupported", "migrate-issues is not supported in proxied-server mode"),
	"gate discover":          refused("proxy.gate.unsupported", "gate discover is not supported in proxied-server mode"),
	"admin cleanup":          refused("proxy.admin.unsupported", "admin cleanup is not supported in proxied-server mode"),
	"admin reset":            refused("proxy.admin.unsupported", "admin reset is not supported in proxied-server mode"),
	"dolt push":              refused("proxy.dolt_push.unsupported", "dolt push is not supported in proxied-server mode"),
	"dolt pull":              refused("proxy.dolt_pull.unsupported", "dolt pull is not supported in proxied-server mode"),
	"dolt commit":            refused("proxy.dolt_commit.unsupported", "dolt commit is not supported in proxied-server mode"),
	"dolt remote":            refused("proxy.dolt_remote.unsupported", "dolt remote is not supported in proxied-server mode"),
	"dolt remote add":        refused("proxy.dolt_remote.unsupported", "dolt remote add is not supported in proxied-server mode"),
	"dolt remote list":       refused("proxy.dolt_remote.unsupported", "dolt remote list is not supported in proxied-server mode"),
	"cook":                   refused("proxy.formula.unsupported", "cook is not supported in proxied-server mode"),
	"ship":                   refused("proxy.formula.unsupported", "ship is not supported in proxied-server mode"),
	"swarm create":           refused("proxy.swarm.unsupported", "swarm create is not supported in proxied-server mode"),
	"swarm list":             refused("proxy.swarm.unsupported", "swarm list is not supported in proxied-server mode"),
	"merge-slot create":      refused("proxy.merge_slot.unsupported", "merge-slot create is not supported in proxied-server mode"),
	"merge-slot check":       refused("proxy.merge_slot.unsupported", "merge-slot check is not supported in proxied-server mode"),
	"merge-slot acquire":     refused("proxy.merge_slot.unsupported", "merge-slot acquire is not supported in proxied-server mode"),
	"merge-slot release":     refused("proxy.merge_slot.unsupported", "merge-slot release is not supported in proxied-server mode"),
	"dolt remote reset-data": refused("proxy.dolt_remote.unsupported", "dolt remote reset-data is not supported in proxied-server mode"),
	"sync":                   refused("proxy.sync.unsupported", "sync is not supported in proxied-server mode"),
}

func init() {
	for parent, children := range map[string][]string{
		"vc":         {"merge", "commit", "status"},
		"federation": {"sync", "status", "add-peer", "remove-peer", "list-peers"},
		"repo":       {"add", "remove", "list", "sync"},
		"conflicts":  {"list", "show", "resolve"},
		"migrate":    {"hooks", "issues"},
	} {
		rule := proxyMaintenanceRefusals[parent]
		for _, child := range children {
			path := parent + " " + child
			proxyMaintenanceRefusals[path] = refused(rule.Code, path+" is not supported in proxied-server mode")
		}
	}
}

// validateProxyMaintenanceBeforeProvider rejects known direct-only commands
// before migrations, auto-start, or provider construction.
func validateProxyMaintenanceBeforeProvider(cmd *cobra.Command) error {
	if cmd == nil {
		return nil
	}
	name := cmd.Name()
	path := strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()))
	if name == "compact" {
		if cmd.Flags().Lookup("dolt") == nil {
			return nil // root `bd compact` is the Dolt history command
		}
		dolt, _ := cmd.Flags().GetBool("dolt")
		if dolt {
			return nil
		}
		rule := proxyMaintenanceRefusals["compact"]
		return HandleProxyCapabilityError(&ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode})
	}
	if rule, ok := proxyMaintenanceRefusals[path]; ok {
		return HandleProxyCapabilityError(&ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode})
	}
	if class, ok := LookupHistoryCapability(path); ok && class == HistoryDirectOnly {
		rule := refused("proxy.history.unsupported", path+" is not supported in proxied-server mode")
		if specific, found := proxyMaintenanceRefusals[path]; found {
			rule = specific
		}
		return HandleProxyCapabilityError(&ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode, Mutates: rule.Mutates})
	}
	if strings.Contains(path, " ") {
		return nil
	}
	if rule, ok := proxyMaintenanceRefusals[name]; ok {
		return HandleProxyCapabilityError(&ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode})
	}
	return nil
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

var proxyCapabilityMatrix = map[ProxyMode]map[ProxyCapability]proxyCapabilityRule{
	ProxyModeDirect: {
		ProxyCapReadonly: honored(), ProxyCapMaxRows: honored(),
		ProxyCapWatch: honored(), ProxyCapRepo: honored(),
	},
	ProxyModeProxied: {
		ProxyCapReadonly: refused("proxy.readonly.unsupported", "strict readonly is unavailable for dolt proxied-server backend; refusing to open a store that cannot guarantee mutation-free access"),
		ProxyCapMaxRows:  refused("proxy.max_rows.unsupported", "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode"),
		ProxyCapWatch:    refused("proxy.watch.unsupported", "watch mode not supported in proxied-server mode"),
		ProxyCapRepo:     refused("proxy.repo.unsupported", "--repo is not supported with --proxied-server"),
	},
}

var proxyCommandCapabilities = map[string]map[ProxyMode]map[ProxyCapability]proxyCapabilityRule{
	"show":            {ProxyModeProxied: {ProxyCapWatch: refused("proxy.watch.unsupported", "watch mode not supported in proxied-server mode")}},
	"list":            {ProxyModeProxied: {ProxyCapWatch: honored(), ProxyCapMaxRows: honored(), ProxyCapRepo: notApplicable()}},
	"dep tree":        {ProxyModeProxied: {ProxyCapMaxRows: honored()}},
	"ready":           {ProxyModeProxied: {ProxyCapMaxRows: refused("proxy.max_rows.unsupported", "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode")}},
	"graph":           {ProxyModeProxied: {ProxyCapMaxRows: refused("proxy.max_rows.unsupported", "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode")}},
	"find-duplicates": {ProxyModeProxied: {ProxyCapMaxRows: refused("proxy.max_rows.unsupported", "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode")}},
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

// AssertProxyCommandCapability checks a command-specific capability rule.
func AssertProxyCommandCapability(command string, mode ProxyMode, capability ProxyCapability) error {
	if commands, ok := proxyCommandCapabilities[command]; ok {
		if modes, ok := commands[mode]; ok {
			if rule, ok := modes[capability]; ok {
				if rule.Outcome == ProxyOutcomeHonored || rule.Outcome == ProxyOutcomeDelegated {
					return nil
				}
				if rule.Code != "" {
					return &ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode, Mutates: rule.Mutates}
				}
				return fmt.Errorf("%s", rule.Message)
			}
		}
	}
	rule, ok := LookupProxyCapability(mode, capability)
	if !ok || (rule.Outcome != ProxyOutcomeHonored && rule.Outcome != ProxyOutcomeDelegated) {
		if rule.Code != "" {
			return &ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode, Mutates: rule.Mutates}
		}
		if rule.Message != "" {
			return fmt.Errorf("%s", rule.Message)
		}
		return fmt.Errorf("%s is not supported in %s mode", capability, mode)
	}
	return nil
}

// validateProxyCapabilitiesBeforeProvider runs refusals that can be decided
// from argv before the proxied provider is opened.
func validateProxyCapabilitiesBeforeProvider(cmd *cobra.Command) error {
	if cmd == nil {
		return nil
	}
	name := cmd.Name()
	if name == "create" && cmd.Flags().Changed("repo") {
		return HandleProxyCapabilityError(AssertProxyCapability(ProxyModeProxied, ProxyCapRepo))
	}
	if name == "show" {
		if watch, _ := cmd.Flags().GetBool("watch"); watch {
			return HandleProxyCapabilityError(AssertProxyCommandCapability("show", ProxyModeProxied, ProxyCapWatch))
		}
	}
	if name == "ready" {
		maxRows, _, err := resolveMaxRows(cmd)
		if err != nil {
			return err
		}
		if maxRows > 0 {
			return HandleProxyCapabilityError(AssertProxyCommandCapability(name, ProxyModeProxied, ProxyCapMaxRows))
		}
	}
	if name == "graph" || name == "find-duplicates" {
		maxRows, _, err := resolveMaxRows(cmd)
		if err != nil {
			return err
		}
		if maxRows > 0 {
			return HandleProxyCapabilityError(AssertProxyCommandCapability(name, ProxyModeProxied, ProxyCapMaxRows))
		}
	}
	return nil
}
