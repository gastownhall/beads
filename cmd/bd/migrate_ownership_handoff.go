package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	handoff "github.com/steveyegge/beads/internal/ownershiphandoffv2"
)

var (
	handoffRoot        string
	handoffDatabase    string
	handoffWorkspace   string
	handoffEndpoint    string
	handoffLegacyPID   int
	handoffLegacyBirth string
	handoffCaller      string
	handoffJSON        bool
)

// migrateOwnershipHandoffCmd transfers a Dolt scope's lifecycle ownership from
// a caller-managed server to bd, one journaled phase per invocation.
//
// It skips the store open on purpose: between legacy-gone and commit the scope
// has no settled owner, and this command is the thing that settles it. Opening
// the store here would deadlock against its own fence.
var migrateOwnershipHandoffCmd = &cobra.Command{
	Use:   "ownership-handoff <phase>",
	Short: "Take over a locally managed Dolt server's lifecycle, one journaled phase at a time",
	Long: `Transfer ownership of a directly managed, loopback Dolt server to bd.

The caller drives the sequence and owns its own server. bd owns the journal, the
replacement server, the fences and the rollback: it never calls the caller back,
and it starts nothing but dolt.

Phases, in order:
  prepare          capture the scope, prove the caller's server serves it, and
                   record the caller's process identity. Mutates nothing.
  legacy-gone      prove the caller's server is stopped: the endpoint is quiet,
                   the captured process is positively gone, Dolt's storage lock
                   is released, and nothing holds the port. Mutates nothing.
  configure        initialize the data dir if needed and launch bd's replacement
                   server on a fresh loopback port, identity-bound.
  verify           prove the replacement serves the same data.
  commit           point the workspace's own files at the replacement, then
                   check bd's own resolvers agree before recording it.

Undo, at any point before commit:
  rollback         stop the replacement by its captured identity, undo the init
                   if bd made it, and restore the workspace byte-exact.
  rollback-finish  admit the caller's server back and retire the journal. Safe
                   to re-run: it refuses without advancing until the caller's
                   server is back.

  status           report the journal. Never mutates.

Every phase is idempotent: re-running a completed one reports the journal, and a
request that conflicts with the journal is refused rather than merged. Exit
status is zero exactly when the journal has reached the requested phase.

Example:
  bd migrate ownership-handoff prepare \
    --root /srv/scope --database scope_db --workspace 8f3c... \
    --legacy-endpoint 127.0.0.1:3307 --json`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	Annotations:   map[string]string{skipStoreAnnotation: "1"},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runOwnershipHandoff(cmd, args[0])
	},
}

func runOwnershipHandoff(cmd *cobra.Command, phase string) error {
	opts := handoff.Options{
		Root:      handoffRoot,
		Database:  handoffDatabase,
		Workspace: handoffWorkspace,
		BDVersion: Version,
		Hints: handoff.Hints{
			LegacyPID:      handoffLegacyPID,
			LegacyPIDBirth: handoffLegacyBirth,
			Caller:         handoffCaller,
		},
	}
	if handoffEndpoint != "" {
		endpoint, err := handoff.ParseEndpoint(handoffEndpoint)
		if err != nil {
			return reportHandoff(handoff.Result{
				SchemaVersion: handoff.SchemaVersion,
				ErrorCode:     handoff.ErrorCode(err),
				Error:         err.Error(),
			}, phase, err)
		}
		opts.Endpoint = endpoint
	}

	result, runErr := handoff.Run(cmd.Context(), phase, opts)
	return reportHandoff(result, phase, runErr)
}

// reportHandoff prints the one result object and decides the exit status. The
// status is derived from the journal, not from whether the call returned an
// error: a verb that finds its phase already reached has nothing to do and has
// succeeded.
func reportHandoff(result handoff.Result, phase string, runErr error) error {
	if handoffJSON {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("encode ownership handoff result: %w", err)
		}
		fmt.Println(string(encoded))
	} else {
		printHandoffText(result)
	}
	if result.Reached(phase) {
		return nil
	}
	if runErr != nil {
		return runErr
	}
	return errors.New("ownership handoff did not reach " + phase)
}

func printHandoffText(result handoff.Result) {
	out := os.Stdout
	if result.ErrorCode != "" {
		out = os.Stderr
	}
	phase := string(result.Phase)
	if phase == "" {
		phase = "(no journal)"
	}
	fmt.Fprintf(out, "ownership handoff: %s (owner %s, mutates=%v)\n", phase, result.Owner, result.Mutates)
	if result.Target.PID > 0 {
		fmt.Fprintf(out, "  replacement: pid %d on %s:%d\n", result.Target.PID, result.Target.Host, result.Target.Port)
	}
	if result.LegacyInstance.Resolved {
		fmt.Fprintf(out, "  legacy instance: pid %d (bound by %s)\n",
			result.LegacyInstance.PID, result.LegacyInstance.BoundBy)
	} else if result.LegacyInstance.Reason != "" {
		fmt.Fprintf(out, "  legacy instance: unresolved (%s)\n", result.LegacyInstance.Reason)
	}
	if result.EvidenceIncomplete {
		fmt.Fprintf(out, "  note: some gates could not be evaluated on this platform; see --json evidence\n")
	}
	if result.ErrorCode != "" {
		fmt.Fprintf(out, "  refused [%s]: %s\n", result.ErrorCode, result.Error)
	}
}

func init() {
	f := migrateOwnershipHandoffCmd.Flags()
	f.StringVar(&handoffRoot, "root", "", "Absolute workspace root containing .beads/ (required)")
	f.StringVar(&handoffDatabase, "database", "", "Dolt database the server serves for this workspace (required)")
	f.StringVar(&handoffWorkspace, "workspace", "", "Project identity that must match .beads/metadata.json (required)")
	f.StringVar(&handoffEndpoint, "legacy-endpoint", "", "host:port where the current owner's server listens; loopback only (required)")
	f.IntVar(&handoffLegacyPID, "legacy-pid", 0, "Hint: the caller's belief about its server's pid")
	f.StringVar(&handoffLegacyBirth, "legacy-pid-birth", "", "Hint: the caller's belief about its server's process birth identity")
	f.StringVar(&handoffCaller, "caller", "", "Hint: free-form caller name, recorded in the journal")
	f.BoolVar(&handoffJSON, "json", false, "Emit the result as a single JSON object")

	migrateOwnershipHandoffCmd.ValidArgs = handoff.Verbs
	migrateOwnershipHandoffCmd.Example = "  bd migrate ownership-handoff " + strings.Join(handoff.Verbs, "|")
	migrateCmd.AddCommand(migrateOwnershipHandoffCmd)
}
