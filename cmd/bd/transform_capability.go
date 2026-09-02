package main

import "github.com/spf13/cobra"

type TransformCapability struct {
	Path     string
	Argument string
	Outcome  ProxyCapabilityOutcome
}

var transformCapabilityRows = []TransformCapability{
	{Path: "rename", Outcome: ProxyOutcomeRefused}, {Path: "rename-prefix", Outcome: ProxyOutcomeRefused},
	{Path: "duplicate", Outcome: ProxyOutcomeRefused}, {Path: "supersede", Outcome: ProxyOutcomeRefused},
	{Path: "duplicates", Argument: "--auto-merge", Outcome: ProxyOutcomeRefused},
	{Path: "duplicates", Argument: "--auto-merge --dry-run", Outcome: ProxyOutcomeHonored},
}

var transformCapabilityMatrix = map[string]proxyCapabilityRule{
	"rename":        refused("proxy.transform.unsupported", "rename is not supported in proxied-server mode"),
	"rename-prefix": refused("proxy.transform.unsupported", "rename-prefix is not supported in proxied-server mode"),
	"duplicate":     refused("proxy.transform.unsupported", "duplicate is not supported in proxied-server mode"),
	"supersede":     refused("proxy.transform.unsupported", "supersede is not supported in proxied-server mode"),
}

func validateProxyTransformBeforeProvider(cmd *cobra.Command) error {
	if cmd == nil {
		return nil
	}
	path := cmd.CommandPath()
	if cmd.Root() != cmd {
		path = path[len(cmd.Root().Name())+1:]
	}
	if rule, ok := transformCapabilityMatrix[path]; ok {
		return HandleProxyCapabilityError(&ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode})
	}
	if path == "duplicates" {
		auto, _ := cmd.Flags().GetBool("auto-merge")
		dry, _ := cmd.Flags().GetBool("dry-run")
		if auto && !dry {
			return HandleProxyCapabilityError(&ProxyCapabilityError{Code: "proxy.transform.unsupported", Message: "duplicates --auto-merge is not supported in proxied-server mode", ExitCode: 1})
		}
	}
	return nil
}
