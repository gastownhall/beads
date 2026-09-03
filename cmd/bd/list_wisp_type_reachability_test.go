package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newWispTypeInputCmd carries only the flags the --wisp-type reachability
// check reads. gatherListInput reads many others, but GetString / GetBool on
// an unregistered flag return zero values, so this stays minimal — the same
// shape as newListInputTestCmd.
func newWispTypeInputCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	f := cmd.Flags()
	f.String("wisp-type", "", "")
	f.Bool("include-ephemeral", false, "")
	f.Bool("include-infra", false, "")
	f.StringP("type", "t", "", "")
	return cmd
}

// TestListRegistersIncludeEphemeral pins the flag the guard below points at.
//
// It is the load-bearing half of this change: ListRequest.IncludeEphemeral is
// the plane knob its own doc names as the combination that answers with rows,
// and `bd list` did not register a flag for it. Without this the advice the
// guard prints names a flag that does not exist, which is worse than the
// silence it replaces.
func TestListRegistersIncludeEphemeral(t *testing.T) {
	if listCmd.Flags().Lookup("include-ephemeral") == nil {
		t.Fatal("bd list must register --include-ephemeral: it is the only narrow way to admit the wisp plane, and --wisp-type's error message tells callers to use it")
	}
}

// TestGatherListInput_WispTypeWithoutAPlaneIsRefused covers the request that
// cannot match a row for ANY input: wisp_type narrows whatever the rest of the
// request admitted, and a default listing admits only durable rows, which
// carry no classification.
//
// The API layer keeps composing this to an empty page — that is its pinned
// contract, and this does not touch it. The refusal is here, where the only
// thing a human can have meant is the combination that returns rows.
func TestGatherListInput_WispTypeWithoutAPlaneIsRefused(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	cmd := newWispTypeInputCmd()
	if err := cmd.Flags().Set("wisp-type", "heartbeat"); err != nil {
		t.Fatalf("set wisp-type: %v", err)
	}
	var err error
	stderr := captureStderr(t, func() { _, err = gatherListInput(cmd) })
	if err == nil {
		t.Fatal("--wisp-type with no plane knob cannot match any row and must be refused, got nil error")
	}
	// The message has to carry the way out, or it is just a different silence.
	if !strings.Contains(stderr, "--include-ephemeral") {
		t.Errorf("the refusal must name the flag that makes the request answerable; got:\n%s", stderr)
	}
}

// TestGatherListInput_WispTypeWithAPlaneIsAccepted is the discriminating half:
// a guard that refused everything would pass the test above on its own. Each
// case here admits the plane by a different route, and all must survive.
func TestGatherListInput_WispTypeWithAPlaneIsAccepted(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	for _, tc := range []struct {
		name string
		flag string
		val  string
	}{
		{"the narrow plane knob", "include-ephemeral", "true"},
		{"the wider bundle that also admits it", "include-infra", "true"},
		// An explicit type is left alone: an infra type routes to the plane by
		// itself, and this layer cannot tell which types those are without the
		// workspace's infra vocabulary. Refusing here would reject a lawful,
		// answerable request.
		{"an explicit type, which may itself be infra", "type", "agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newWispTypeInputCmd()
			if err := cmd.Flags().Set("wisp-type", "heartbeat"); err != nil {
				t.Fatalf("set wisp-type: %v", err)
			}
			if err := cmd.Flags().Set(tc.flag, tc.val); err != nil {
				t.Fatalf("set %s: %v", tc.flag, err)
			}
			in, err := gatherListInput(cmd)
			if err != nil {
				t.Fatalf("--wisp-type with --%s must be accepted, got: %v", tc.flag, err)
			}
			if in.WispType == nil || string(*in.WispType) != "heartbeat" {
				t.Errorf("WispType must survive onto the request, got %v", in.WispType)
			}
		})
	}
}

// TestGatherListInput_IncludeEphemeralReachesTheRequest confirms the new flag
// is wired through rather than merely registered — a flag that parses but is
// never read would leave the plane just as unreachable.
func TestGatherListInput_IncludeEphemeralReachesTheRequest(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	cmd := newWispTypeInputCmd()
	if err := cmd.Flags().Set("include-ephemeral", "true"); err != nil {
		t.Fatalf("set include-ephemeral: %v", err)
	}
	in, err := gatherListInput(cmd)
	if err != nil {
		t.Fatalf("gatherListInput: %v", err)
	}
	if !in.IncludeEphemeral {
		t.Error("--include-ephemeral must set ListRequest.IncludeEphemeral, got false")
	}

	// And the default stays off, so the durable-only listing is unchanged.
	bare, err := gatherListInput(newWispTypeInputCmd())
	if err != nil {
		t.Fatalf("gatherListInput (bare): %v", err)
	}
	if bare.IncludeEphemeral {
		t.Error("IncludeEphemeral must default to false; the default listing is durable-only")
	}
}
