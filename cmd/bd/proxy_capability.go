package main

import "fmt"

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

type proxyCapabilityRule struct {
	Supported bool
	Message   string
}

var proxyCapabilityMatrix = map[ProxyMode]map[ProxyCapability]proxyCapabilityRule{
	ProxyModeDirect: {
		ProxyCapReadonly: {Supported: true}, ProxyCapMaxRows: {Supported: true},
		ProxyCapWatch: {Supported: true}, ProxyCapRepo: {Supported: true},
	},
	ProxyModeProxied: {
		ProxyCapReadonly: {Supported: true}, ProxyCapMaxRows: {Supported: true},
		ProxyCapWatch: {Message: "watch mode not supported in proxied-server mode"},
		ProxyCapRepo:  {Message: "--repo is not supported with --proxied-server"},
	},
}

var proxyCommandCapabilities = map[string]map[ProxyMode]map[ProxyCapability]proxyCapabilityRule{
	"show": {ProxyModeProxied: {ProxyCapWatch: {Message: "watch mode not supported in proxied-server mode"}}},
	"list": {ProxyModeProxied: {ProxyCapWatch: {Supported: true}}},
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
				if rule.Supported {
					return nil
				}
				return fmt.Errorf("%s", rule.Message)
			}
		}
	}
	rule, ok := LookupProxyCapability(mode, capability)
	if !ok || !rule.Supported {
		if rule.Message != "" {
			return fmt.Errorf("%s", rule.Message)
		}
		return fmt.Errorf("%s is not supported in %s mode", capability, mode)
	}
	return nil
}
