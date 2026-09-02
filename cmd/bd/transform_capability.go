package main

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type TransformCapability struct {
	Path     string
	Argument string
	Outcome  ProxyCapabilityOutcome
	Code     string
	Message  string
	ExitCode int
	Mutates  bool
}

var transformCapabilityRows = []TransformCapability{
	transformRefused("rename"),
	transformRefused("rename-prefix"),
	transformRefused("duplicate"),
	transformRefused("supersede"),
	{Path: "duplicates", Outcome: ProxyOutcomeHonored},
	transformRefusedWithArgs("duplicates", "--auto-merge"),
	{Path: "duplicates", Argument: "--auto-merge --dry-run", Outcome: ProxyOutcomeHonored},
}

func transformRefused(path string) TransformCapability {
	return transformRefusedWithArgs(path, "")
}

func transformRefusedWithArgs(path, argument string) TransformCapability {
	displayPath := path
	if argument != "" {
		displayPath += " " + argument
	}
	return TransformCapability{
		Path: path, Argument: argument, Outcome: ProxyOutcomeRefused,
		Code: "proxy.transform.unsupported", Message: displayPath + " is not supported in proxied-server mode", ExitCode: 1,
	}
}

func transformCapabilityError(row TransformCapability) *ProxyCapabilityError {
	return &ProxyCapabilityError{Code: row.Code, Message: row.Message, ExitCode: row.ExitCode, Mutates: row.Mutates}
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
	if row, ok := lookupTransformCapability(cmd); ok {
		if row.Outcome == ProxyOutcomeRefused {
			return HandleProxyCapabilityError(transformCapabilityError(row))
		}
	}
	return nil
}
