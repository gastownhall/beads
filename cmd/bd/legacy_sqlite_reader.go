package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/migration/legacysqlite"
)

var legacySQLiteCmd = &cobra.Command{
	Use:           "legacy-sqlite --source-db PATH --output PATH|-",
	Short:         "Read an authenticated legacy SQLite database as JSONL",
	Annotations:   map[string]string{skipStoreAnnotation: "1"},
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		source, _ := cmd.Flags().GetString("source-db")
		output, _ := cmd.Flags().GetString("output")
		if source == "" || output == "" {
			return fmt.Errorf("--source-db and --output are required")
		}
		return legacysqlite.Export(cmd.Context(), source, output, os.Stdout)
	},
}

func init() {
	migrateCmd.AddCommand(legacySQLiteCmd)
	legacySQLiteCmd.Flags().String("source-db", "", "Legacy SQLite database (read-only)")
	legacySQLiteCmd.Flags().String("output", "", "JSONL output path, or - for stdout")
}
