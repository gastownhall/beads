package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/gqlapi"
	"github.com/steveyegge/beads/internal/metrics"
)

var gqlCmd = &cobra.Command{
	Use:     "gql [document]",
	GroupID: "issues",
	Short:   "Query issues with a read-only GraphQL document",
	Long: `Query issues with a read-only GraphQL document and print only the
fields it selects.

The response is one JSON object on stdout:

  {"data": ..., "errors": [...], "extensions": {"schema_version": N}}

"errors" is omitted when there are none. bd gql exits 1 when "errors" is
present. The schema has no mutations. Like bd query and bd ready, bd gql
runs bd's normal startup maintenance (version tracking, auto-migration and
auto-backup when enabled), and ready wakes expired deferred issues as
bd ready does. Outside proxied-server mode, global --readonly turns the
maintenance off; a proxied-server workspace refuses --readonly.

The document comes from one positional argument, --file PATH, or --stdin.
Use --vars with a JSON object for GraphQL variables. Relation dependencies
are returned as the reader provides them; the dependency list can be shorter
than dependency_count. Classified reads skip auto-push and auto-export.

Each request is limited to a 16384-byte document, depth 8, list limit 1 to
200, 200 store reads, and a response of at most 10,000 issues, relations and
comments and 64 MiB of long text. A request over a limit gets an "errors"
entry that names it. docs/reference/graphql.md describes the limits.

Examples:
  bd gql '{ issue(id: "bd-1") { id title status created_at } }'
  bd gql --file query.graphql --vars '{"id":"bd-1"}'`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("gql")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		sources := cmdTextSources(cmd, args)
		// The shared resolver ignores a blank positional when checking for
		// conflicting sources, but it still counts as a supplied source here.
		if len(args) > 0 && (sources.stdin != nil || sources.filePath != "") {
			return HandleErrorRespectJSON("cannot combine positional GraphQL document with --stdin or --file")
		}
		if len(args) == 0 && sources.stdin == nil && sources.filePath == "" {
			fmt.Fprintf(os.Stderr, "Error: GraphQL document is required\n\n")
			if err := cmd.Help(); err != nil {
				fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
			}
			return SilentExit()
		}
		document, err := requireTextFromSources("GraphQL document", "use a positional document, --stdin, or --file", sources)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		var variables map[string]any
		if raw, _ := cmd.Flags().GetString("vars"); raw != "" || cmd.Flags().Changed("vars") {
			if err := json.Unmarshal([]byte(raw), &variables); err != nil || variables == nil {
				return HandleErrorRespectJSON("--vars must be a JSON object")
			}
		}

		reader, err := openIssueReader()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		querier, err := openQuerier()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		return runGQL(cmd, gqlapi.Roles{Reader: reader, Querier: querier}, document, variables)
	},
}

// runGQL executes one document and prints graphql-go's response as the
// command's whole output. schema_version is set here, on the response itself,
// so every response carries it, error responses included, whatever
// BD_JSON_ENVELOPE says.
func runGQL(cmd *cobra.Command, roles gqlapi.Roles, document string, variables map[string]any) error {
	resp := gqlapi.Execute(rootCtx, roles, document, variables)
	if resp.Extensions == nil {
		resp.Extensions = map[string]any{}
	}
	resp.Extensions["schema_version"] = JSONSchemaVersion

	out, err := json.Marshal(resp)
	if err != nil {
		return HandleErrorRespectJSON("encoding GraphQL response: %v", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(out))
	if len(resp.Errors) > 0 {
		return SilentExit()
	}
	return nil
}

func init() {
	registerTextSourceFlags(gqlCmd, "GraphQL document")
	gqlCmd.Flags().String("vars", "", "GraphQL variables as a JSON object")
	rootCmd.AddCommand(gqlCmd)
}
