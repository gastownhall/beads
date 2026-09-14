package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The vocabulary #5739 ruled on: the filter flag NAMES whose repeat semantics
// are now decided. A plain pflag string under one of these names is the defect
// si-hcjy found — a repeat that keeps only the last value and says nothing.
var filterFlagVocabulary = map[string]bool{
	"status":   true,
	"state":    true,
	"id":       true,
	"type":     true,
	"assignee": true,
	"label":    true,
}

// filterFlagSetters is the reasoned exemption set: flags in the vocabulary
// that are a VALUE the command writes, not a filter it reads. Last-one-wins is
// the shell's own override idiom on a setter (`bd create $DEFAULTS --type bug`)
// and narrows nothing, so these stay plain strings.
//
// It is an exemption list, not a checklist: the guard below fails CLOSED, so a
// new --status/--type/--id/--state/--assignee/--label that is neither a union
// flag, a once flag, nor listed here breaks the build and its author has to say
// which kind it is.
var filterFlagSetters = map[string]string{
	"bd create --type":       "the type the new issue gets",
	"bd create --status":     "the status the new issue gets",
	"bd create --id":         "the explicit id the new issue gets",
	"bd create --assignee":   "the assignee the new issue gets",
	"bd update --type":       "the new type",
	"bd update --status":     "the new status",
	"bd update --assignee":   "the new assignee",
	"bd q --type":            "the type the quick-created issue gets",
	"bd dep add --type":      "the dependency type being created",
	"bd link --type":         "the dependency type being created",
	"bd gate create --type":  "the gate type being created",
	"bd gate check --type":   "which gate kind to evaluate, not a filter over rows",
	"bd mol bond --type":     "the bond type being created",
	"bd mol pour --assignee": "who the poured root issue is assigned to",
	"bd audit label --label": "the label VALUE being recorded",
	"bd admin compact --id":  "the single issue to compact",
}

// walkFilterFlags visits every flag in the vocabulary on every command in the
// tree, deriving the sweep's subject matter from the flag registrations
// themselves rather than from a list anyone keeps by hand.
func walkFilterFlags(t *testing.T, visit func(path string, cmd *cobra.Command, f *pflag.Flag)) {
	t.Helper()
	var walk func(c *cobra.Command, parent string)
	walk = func(c *cobra.Command, parent string) {
		path := c.Name()
		if parent != "" {
			path = parent + " " + c.Name()
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if filterFlagVocabulary[f.Name] {
				visit(path, c, f)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub, path)
		}
	}
	walk(rootCmd, "")
}

// TestNoPlainStringFilterFlagSurvives is the sweep's invariant: a repeated
// filter flag must union or refuse, never silently discard. A plain
// *pflag.stringValue under a vocabulary name does exactly the discarding, so
// its presence anywhere in the tree is the defect itself.
func TestNoPlainStringFilterFlagSurvives(t *testing.T) {
	var offenders []string
	walkFilterFlags(t, func(path string, _ *cobra.Command, f *pflag.Flag) {
		switch f.Value.(type) {
		case *unionStringFlag, *onceStringFlag:
			return
		}
		if f.Value.Type() != "string" {
			// stringSlice/stringArray/int values are repeat-safe or
			// repeat-meaningless already; only a scalar string discards. They
			// need no exemption, which is why none is listed for them.
			return
		}
		key := path + " --" + f.Name
		if _, exempt := filterFlagSetters[key]; exempt {
			return
		}
		offenders = append(offenders, fmt.Sprintf("%s (%q)", key, f.Usage))
	})
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("%d filter flag(s) still keep only the last value on repeat:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// TestRepeatedUnionFilterFlagsUnion and TestRepeatedOnceFilterFlagsRefuse
// derive their cases from the registrations too: every flag that got a union
// value must actually union, every flag that got a once value must actually
// refuse. Nothing here names a subcommand.
func TestRepeatedUnionFilterFlagsUnion(t *testing.T) {
	n := 0
	walkFilterFlags(t, func(path string, cmd *cobra.Command, f *pflag.Flag) {
		if _, ok := f.Value.(*unionStringFlag); !ok {
			return
		}
		n++
		t.Run(path+" --"+f.Name, func(t *testing.T) {
			resetAllFilterFlags()
			t.Cleanup(resetAllFilterFlags)
			if err := cmd.ParseFlags([]string{"--" + f.Name, "aaa", "--" + f.Name, "bbb"}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			got, err := cmd.Flags().GetString(f.Name)
			if err != nil {
				t.Fatalf("GetString(%s): %v", f.Name, err)
			}
			if got != "aaa,bbb" {
				t.Fatalf("repeated --%s = %q, want the union %q", f.Name, got, "aaa,bbb")
			}
		})
	})
	if n == 0 {
		t.Fatal("no union filter flags found: the derivation matched nothing, so this test proved nothing")
	}
}

func TestRepeatedOnceFilterFlagsRefuse(t *testing.T) {
	n := 0
	walkFilterFlags(t, func(path string, cmd *cobra.Command, f *pflag.Flag) {
		if _, ok := f.Value.(*onceStringFlag); !ok {
			return
		}
		n++
		t.Run(path+" --"+f.Name, func(t *testing.T) {
			resetAllFilterFlags()
			t.Cleanup(resetAllFilterFlags)
			err := cmd.ParseFlags([]string{"--" + f.Name, "aaa", "--" + f.Name, "bbb"})
			if err == nil {
				t.Fatalf("repeated --%s was accepted; it must refuse rather than keep one value", f.Name)
			}
			if !strings.Contains(err.Error(), f.Name) || !strings.Contains(err.Error(), "more than once") {
				t.Fatalf("error %q does not name the repeated flag", err)
			}
		})
	})
	if n == 0 {
		t.Fatal("no once filter flags found: the derivation matched nothing, so this test proved nothing")
	}
}

// A single value and the comma form must parse exactly as they did before the
// sweep — a fix that unions must not quietly become "ignore the filter"
// (si-hcjy acceptance D).
func TestSweptFilterFlagsKeepSingleAndCommaForms(t *testing.T) {
	walkFilterFlags(t, func(path string, cmd *cobra.Command, f *pflag.Flag) {
		switch f.Value.(type) {
		case *unionStringFlag, *onceStringFlag:
		default:
			return
		}
		t.Run(path+" --"+f.Name, func(t *testing.T) {
			resetAllFilterFlags()
			t.Cleanup(resetAllFilterFlags)
			if err := cmd.ParseFlags([]string{"--" + f.Name, "aaa"}); err != nil {
				t.Fatalf("ParseFlags single: %v", err)
			}
			if got, _ := cmd.Flags().GetString(f.Name); got != "aaa" {
				t.Fatalf("single --%s = %q, want %q", f.Name, got, "aaa")
			}
			resetAllFilterFlags()
			if err := cmd.ParseFlags([]string{"--" + f.Name, "aaa,bbb"}); err != nil {
				t.Fatalf("ParseFlags comma: %v", err)
			}
			if got, _ := cmd.Flags().GetString(f.Name); got != "aaa,bbb" {
				t.Fatalf("comma --%s = %q, want %q", f.Name, got, "aaa,bbb")
			}
		})
	})
}

// resetAllFilterFlags must restore a flag's DEFAULT, not the empty string:
// `bd jira sync --state` defaults to "all", and a reset that emptied it would
// turn the next in-process run's default from "all statuses" into "none".
func TestResetRestoresDefaults(t *testing.T) {
	checked := 0
	walkFilterFlags(t, func(path string, cmd *cobra.Command, f *pflag.Flag) {
		switch f.Value.(type) {
		case *unionStringFlag, *onceStringFlag:
		default:
			return
		}
		if f.DefValue == "" {
			return
		}
		checked++
		t.Run(path+" --"+f.Name, func(t *testing.T) {
			t.Cleanup(resetAllFilterFlags)
			if err := cmd.ParseFlags([]string{"--" + f.Name, "aaa"}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			resetAllFilterFlags()
			if got, _ := cmd.Flags().GetString(f.Name); got != f.DefValue {
				t.Fatalf("--%s after reset = %q, want default %q", f.Name, got, f.DefValue)
			}
			if cmd.Flags().Changed(f.Name) {
				t.Fatalf("--%s still reads as Changed after reset", f.Name)
			}
		})
	})
	if checked == 0 {
		t.Fatal("no swept filter flag carries a non-empty default: this test proved nothing")
	}
}

// The exemption list must not outlive the flags it exempts: an entry naming a
// command or flag that no longer exists is a stale second spelling.
func TestFilterFlagSetterExemptionsAllExist(t *testing.T) {
	seen := map[string]bool{}
	walkFilterFlags(t, func(path string, _ *cobra.Command, f *pflag.Flag) {
		seen[path+" --"+f.Name] = true
	})
	var stale []string
	for key := range filterFlagSetters {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("exemption(s) for flags that no longer exist:\n  %s", strings.Join(stale, "\n  "))
	}
}
