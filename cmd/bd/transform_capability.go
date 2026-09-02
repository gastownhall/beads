package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sort"
	"strings"
)

type TransformCapability struct {
	Path     string
	Argument string
	Outcome  ProxyCapabilityOutcome
}

var transformCapabilityRows = []TransformCapability{
	{Path: "rename", Outcome: ProxyOutcomeRefused}, {Path: "rename-prefix", Outcome: ProxyOutcomeRefused},
	{Path: "duplicate", Outcome: ProxyOutcomeRefused}, {Path: "supersede", Outcome: ProxyOutcomeRefused},
	{Path: "duplicates", Outcome: ProxyOutcomeHonored},
	{Path: "duplicates", Argument: "--auto-merge", Outcome: ProxyOutcomeRefused},
	{Path: "duplicates", Argument: "--auto-merge --dry-run", Outcome: ProxyOutcomeHonored},
}

var transformCapabilityMatrix = map[string]proxyCapabilityRule{
	"rename":        refused("proxy.transform.unsupported", "rename is not supported in proxied-server mode"),
	"rename-prefix": refused("proxy.transform.unsupported", "rename-prefix is not supported in proxied-server mode"),
	"duplicate":     refused("proxy.transform.unsupported", "duplicate is not supported in proxied-server mode"),
	"supersede":     refused("proxy.transform.unsupported", "supersede is not supported in proxied-server mode"),
}

func lookupTransformCapability(cmd *cobra.Command) (TransformCapability, bool) {
	path := cmd.CommandPath()
	path = strings.TrimSpace(strings.TrimPrefix(path, cmd.Root().Name()))
	var args []string
	cmd.Flags().Visit(func(f *pflag.Flag) { args = append(args, "--"+f.Name) })
	sort.Strings(args)
	arg := strings.Join(args, " ")
	for _, row := range transformCapabilityRows {
		if row.Path == path && row.Argument == arg {
			return row, true
		}
	}
	return TransformCapability{}, false
}

func validateProxyTransformBeforeProvider(cmd *cobra.Command) error {
	if cmd == nil {
		return nil
	}
	path := cmd.CommandPath()
	path = strings.TrimSpace(strings.TrimPrefix(path, cmd.Root().Name()))
	if row, ok := lookupTransformCapability(cmd); ok && row.Outcome == ProxyOutcomeRefused {
		return HandleProxyCapabilityError(&ProxyCapabilityError{Code: "proxy.transform.unsupported", Message: path + " is not supported in proxied-server mode", ExitCode: 1})
	}
	if rule, ok := transformCapabilityMatrix[path]; ok {
		return HandleProxyCapabilityError(&ProxyCapabilityError{Code: rule.Code, Message: rule.Message, ExitCode: rule.ExitCode})
	}
	if path == "duplicates" {
		return nil
	}
	return nil
}
