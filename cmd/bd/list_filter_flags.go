package main

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
)

// unionStringFlag accumulates repeated flag occurrences into a comma-joined
// set: `--status open --status closed` means the same union as
// `--status open,closed`. Setting the empty string clears it — `bd children`
// resets listCmd's flags that way after borrowing them, and gatherListInput
// resets after each read so in-process callers that Execute() more than once
// do not inherit the previous command's filter.
type unionStringFlag struct {
	value string
}

func (f *unionStringFlag) Set(s string) error {
	switch {
	case s == "":
		f.value = ""
	case f.value == "":
		f.value = s
	default:
		for _, tok := range strings.Split(s, ",") {
			if !slicesContains(strings.Split(f.value, ","), tok) {
				f.value += "," + tok
			}
		}
	}
	return nil
}

func slicesContains(list []string, want string) bool {
	for _, have := range list {
		if have == want {
			return true
		}
	}
	return false
}

func (f *unionStringFlag) String() string { return f.value }

// Type reports "string" so cmd.Flags().GetString keeps working on flags
// registered with this value.
func (f *unionStringFlag) Type() string { return "string" }

// onceStringFlag refuses a repeated occurrence outright. It belongs on
// filters that are single-valued all the way down (--type, --assignee),
// where comma-joining would either fail type validation on the joined value
// or exact-match nothing and return an empty result that reads as a clean
// answer. Empty-string reset semantics match unionStringFlag.
type onceStringFlag struct {
	name  string
	value string
	set   bool
}

func (f *onceStringFlag) Set(s string) error {
	if s == "" {
		f.value = ""
		f.set = false
		return nil
	}
	if f.set {
		return fmt.Errorf("--%s given more than once (already %q); pass a single value", f.name, f.value)
	}
	f.set = true
	f.value = s
	return nil
}

func (f *onceStringFlag) String() string { return f.value }

func (f *onceStringFlag) Type() string { return "string" }

var (
	listStatusFlag   unionStringFlag
	listStateFlag    unionStringFlag
	listIDFlag       unionStringFlag
	listTypeFlag     = onceStringFlag{name: "type"}
	listAssigneeFlag = onceStringFlag{name: "assignee"}
)

// resetListFilterFlags returns listCmd's five filter flags to their unparsed
// state: the values are cleared AND pflag's Changed bit is lowered on the
// given flag set, so an in-process caller that Execute()s listCmd twice sees
// neither the previous filter nor a stale "the user passed --status" signal.
// It takes the flag set as an argument rather than reading listCmd so that
// listCmd's initializer (via runListCore -> gatherListInput) does not refer
// back to itself.
func resetListFilterFlags(flags *pflag.FlagSet) {
	_ = listStatusFlag.Set("")
	_ = listStateFlag.Set("")
	_ = listIDFlag.Set("")
	_ = listTypeFlag.Set("")
	_ = listAssigneeFlag.Set("")
	if flags == nil {
		return
	}
	for _, name := range []string{"status", "state", "type", "assignee", "id"} {
		if fl := flags.Lookup(name); fl != nil {
			fl.Changed = false
		}
	}
}
