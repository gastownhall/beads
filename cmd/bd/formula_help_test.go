package main

import (
	"strings"
	"testing"
)

func TestFormulaHelpUsesGenericWorkflowLanguage(t *testing.T) {
	help := formulaCmd.Long + "\n" + formulaListCmd.Long + "\n" + molSeedCmd.Long

	for _, want := range []string{
		"Formulas are TOML/JSON files",
		"Define formulas, cook them into protos, then pour or wisp them into work.",
		"shared workspace root",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("formula help missing %q:\n%s", want, help)
		}
	}

	for _, forbidden := range []string{
		"YAML/" + "JSON files",
		"Rig:",
		"Formula " + "workflow:",
		"Author" + ":",
		"Compile: " + "Resolve",
		"Instantiate: " + "Agents",
		"orchestrator, if " + "GT_ROOT set",
		"orchestrator level, if " + "GT_ROOT set",
	} {
		if strings.Contains(help, forbidden) {
			t.Fatalf("formula help still contains %q:\n%s", forbidden, help)
		}
	}
}
