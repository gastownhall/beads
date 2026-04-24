package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// initSafetyHelpCmd documents the init flag surface and the destroy-token
// format. Its contents are the authoritative runtime reference for the
// error-message-no-echo invariant: refusal texts point here rather than
// constructing copy-pasteable destructive invocations. See
// docs/adr/0002-init-safety-invariants.md.
var initSafetyHelpCmd = &cobra.Command{
	Use:   "init-safety",
	Short: "Explain bd init flag semantics and the destroy-token format",
	Long: `bd init flag safety contract.

Every bd init invocation resolves project_id from exactly one explicitly
named source (local reinit, remote adoption, or a fresh mint). When the
source is ambiguous, bd init refuses.

FLAG SURFACE

  bd init                       Mint a new identity. Bootstraps from
                                origin if it has refs/dolt/data.

  bd init --reinit-local        Re-initialize local .beads/ over existing
                                local data. Does NOT authorize discarding
                                remote history. If origin has Dolt data
                                this will refuse — pair with
                                --discard-remote to override.

  bd init --reinit-local \      Discard the remote's Dolt history and
      --discard-remote          replace it with the local reinit. First
                                bd dolt push after this will be a
                                history-replacing force-push.

  bd init --force               Deprecated alias for --reinit-local.
                                Kept working for ≥2 releases.

ADOPTING A REMOTE

  If you want to use the remote's existing history, use:

      bd bootstrap

  bd init will automatically suggest this when a remote is detected.

DESTROY-TOKEN (non-interactive only)

  When running with no TTY (CI, agents, piped input), --discard-remote
  requires an explicit --destroy-token value. The token format is:

      DESTROY-<issue-prefix>

  For example, if your issue prefix is "bd", the token is "DESTROY-bd":

      bd init --reinit-local --discard-remote --destroy-token=DESTROY-bd

  In interactive (TTY) mode you confirm via a typed prompt instead. The
  token is not echoed by bd's runtime error messages — this is a
  deliberate guard against pattern-matched one-liners (see
  docs/adr/0002-init-safety-invariants.md).

EXIT CODES

  10    refused: remote has Dolt history and you passed --force/--reinit-local
        without --discard-remote
  11    refused: existing local data and you declined the destroy confirm
  12    refused: --discard-remote passed without a valid --destroy-token
        (non-interactive mode)

RECOVERY

  If you hit a refusal, see docs/RECOVERY.md for step-by-step recovery
  playbooks for each exit code.
`,
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Print(cmd.Long)
	},
}

func init() {
	rootCmd.AddCommand(initSafetyHelpCmd)
}
