package main

import (
	_ "embed"
)

// Command doc supplements are extra reference Markdown appended after a
// command's flags in the generated CLI docs (bd help --docs-root and the
// per-command staging tree). They are embedded sources, not hand edits:
// regeneration always reproduces them, so the docs drift gate stays
// byte-exact. Supplements are raw Markdown (fenced code allowed) and are
// emitted without MDX escaping — keep prose MDX-safe outside code fences.

//go:embed help_supplements/create_graph_plan.md
var createGraphPlanSupplement string

// commandDocSupplements maps a cobra CommandPath() to its supplement.
var commandDocSupplements = map[string]string{
	"bd create": createGraphPlanSupplement,
}
