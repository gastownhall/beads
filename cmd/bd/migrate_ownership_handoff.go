package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ownershiphandoff"
)

// ownershipHandoffIdentity is the stable operator-facing identity rendered by
// the handoff front door. It intentionally keeps provider state out of the
// wire shape; lifecycle details belong to ownershiphandoff.Hooks.
type ownershipHandoffIdentity struct {
	CityRoot  string                    `json:"city_root"`
	Root      string                    `json:"root"`
	Database  string                    `json:"database"`
	Workspace string                    `json:"workspace"`
	Endpoint  ownershiphandoff.Endpoint `json:"endpoint"`
}

// ownershipHandoffOutput is deliberately separate from the generic bd JSON
// envelope. Handoff is used by lifecycle tooling, so its fields and nesting
// are stable and error_code is present on both success and failure. Error
// carries the same refusal text the text renderer writes to stderr: several
// refusals — an identity conflict above all — say which journal is being
// refused and whether discarding it is safe, and a --json caller has nowhere
// else to read that.
type ownershipHandoffOutput struct {
	Phase     ownershiphandoff.Phase   `json:"phase"`
	Owner     ownershiphandoff.Owner   `json:"owner"`
	Mutates   bool                     `json:"mutates"`
	Identity  ownershipHandoffIdentity `json:"identity"`
	ErrorCode string                   `json:"error_code"`
	Error     string                   `json:"error"`
}

// ownershipHandoffProvider invokes only the explicit GC handoff protocol. It
// resolves GC_BIN after request/journal validation and while the journal lock
// is held; absent or untrusted binaries fail closed without process probing.
var ownershipHandoffProvider ownershiphandoff.Provider = ownershiphandoff.NewGCProviderFromEnv()

var ownershipHandoffCmd = &cobra.Command{
	Use:   "ownership-handoff",
	Short: "Explicitly hand a legacy local Dolt owner to bd",
	Long: `Explicitly hand a legacy local Dolt owner to bd.

The handoff is journaled at <root>/ownership-handoff.json and always resumes
from the last durable checkpoint that journal records, so re-running the same
command after a failure or a crash is the retry: there is no separate resume
mode and no flag to ask for one. A committed journal replays as a no-op, and a
journal belonging to a different identity is refused rather than resumed.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	Annotations: map[string]string{
		skipStoreAnnotation:       "1",
		skipLegacyGuardAnnotation: "1",
	},
	RunE: runOwnershipHandoffCommand,
}

const ownershipHandoffJournalName = "ownership-handoff.json"

func runOwnershipHandoffCommand(cmd *cobra.Command, _ []string) error {
	root, _ := cmd.Flags().GetString("root")
	cityRoot, _ := cmd.Flags().GetString("city")
	database, _ := cmd.Flags().GetString("database")
	workspace, _ := cmd.Flags().GetString("workspace")
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	socket, _ := cmd.Flags().GetString("socket")
	journal, _ := cmd.Flags().GetString("journal")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if socket != "" {
		if cmd.Flags().Changed("host") || cmd.Flags().Changed("port") {
			err := errors.New("socket endpoint cannot be combined with explicit --host or --port")
			result := ownershiphandoff.Result{Phase: ownershiphandoff.PhasePrepared, Owner: ownershiphandoff.OwnerLegacyGC,
				CityRoot: cityRoot, Root: root, Database: database, Workspace: workspace,
				Endpoint: ownershiphandoff.Endpoint{Host: host, Port: port, Socket: socket}, ErrorCode: "invalid_request"}
			if outputErr := writeOwnershipHandoffOutput(result, err); outputErr != nil {
				return outputErr
			}
			return &exitError{Code: 1}
		}
		host = ""
		port = 0
	}
	request := ownershiphandoff.Request{
		CityRoot:  cityRoot,
		Root:      root,
		Database:  database,
		Workspace: workspace,
		Endpoint:  ownershiphandoff.Endpoint{Host: host, Port: port, Socket: socket},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	if journal == "" {
		journal = filepath.Join(root, ownershipHandoffJournalName)
	}
	// An empty or non-canonical city root is refused by ValidateRequest, before
	// Run touches a journal or opens a provider, so the front door does not
	// carry a second copy of that check with its own wording.
	if journal != filepath.Join(root, ownershipHandoffJournalName) {
		err := errors.New("journal must be the canonical <root>/ownership-handoff.json path")
		result := ownershiphandoff.Result{Phase: ownershiphandoff.PhasePrepared, Owner: ownershiphandoff.OwnerLegacyGC,
			CityRoot: cityRoot, Root: root, Database: database, Workspace: workspace, Endpoint: request.Endpoint, ErrorCode: "invalid_journal"}
		if outputErr := writeOwnershipHandoffOutput(result, err); outputErr != nil {
			return outputErr
		}
		return &exitError{Code: 1}
	}
	result, err := ownershiphandoff.Run(getRootContext(), request, journal, ownershipHandoffProvider, dryRun)
	if outputErr := writeOwnershipHandoffOutput(result, err); outputErr != nil {
		return outputErr
	}
	if err != nil {
		return &exitError{Code: 1}
	}
	return nil
}

func writeOwnershipHandoffOutput(result ownershiphandoff.Result, runErr error) error {
	output := ownershipHandoffOutput{
		Phase:   result.Phase,
		Owner:   result.Owner,
		Mutates: result.Mutates,
		Identity: ownershipHandoffIdentity{
			CityRoot:  result.CityRoot,
			Root:      result.Root,
			Database:  result.Database,
			Workspace: result.Workspace,
			Endpoint:  result.Endpoint,
		},
		ErrorCode: result.ErrorCode,
	}
	if runErr != nil {
		output.Error = runErr.Error()
	}
	if jsonOutput {
		if err := outputJSONRaw(output); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(os.Stdout, "ownership handoff: phase=%s owner=%s mutates=%t city=%s root=%s database=%s workspace=%s endpoint=%s error_code=%s\n",
			output.Phase, output.Owner, output.Mutates, output.Identity.CityRoot, output.Identity.Root, output.Identity.Database,
			output.Identity.Workspace, output.Identity.Endpoint.String(), output.ErrorCode)
	}
	if runErr != nil && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Error: ownership handoff: %v\n", runErr)
	}
	return nil
}

func init() {
	ownershipHandoffCmd.Flags().String("root", "", "Canonical Dolt root being handed off")
	ownershipHandoffCmd.Flags().String("city", "", "Canonical Gas City root owning the legacy server")
	ownershipHandoffCmd.Flags().String("database", "", "Database identity being handed off")
	ownershipHandoffCmd.Flags().String("workspace", "", "Workspace identity being handed off")
	ownershipHandoffCmd.Flags().String("host", "127.0.0.1", "Loopback host for the legacy server")
	ownershipHandoffCmd.Flags().Int("port", 3307, "Port for the legacy loopback server")
	ownershipHandoffCmd.Flags().String("socket", "", "Unix socket beneath --root (alternative to --host/--port)")
	ownershipHandoffCmd.Flags().String("journal", "", "Handoff journal path (only canonical <root>/ownership-handoff.json is accepted)")
	ownershipHandoffCmd.Flags().Bool("dry-run", false, "Validate identity without opening a provider or mutating state")
	migrateCmd.AddCommand(ownershipHandoffCmd)
}
