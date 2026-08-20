package main

import "fmt"

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
		f.value += "," + s
	}
	return nil
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

func resetListFilterFlags() {
	_ = listStatusFlag.Set("")
	_ = listStateFlag.Set("")
	_ = listIDFlag.Set("")
	_ = listTypeFlag.Set("")
	_ = listAssigneeFlag.Set("")
}
