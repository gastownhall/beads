package main

import (
	"encoding/json"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// orchestratorMetadataPrefix is the metadata key namespace reserved for
// orchestrator-managed workflow instance beads (Gas City). Beads carrying
// keys in this namespace (gc.kind, gc.step_id, gc.root_bead_id,
// gc.logical_bead_id, gc.control_for, gc.spec_for, ...) are template
// instances whose lifecycle is owned by the orchestrator: a spec bead, a
// logical step bead, and an iteration control bead for the same step
// legitimately share identical title and description text, and per-run
// instances repeat that text across runs. Content-hash deduplication must
// not adjudicate them.
const orchestratorMetadataPrefix = "gc."

// hasOrchestratorMetadata reports whether an issue carries orchestrator
// workflow metadata (any "gc."-prefixed key in its metadata object).
func hasOrchestratorMetadata(issue *types.Issue) bool {
	if len(issue.Metadata) == 0 {
		return false
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(issue.Metadata, &meta); err != nil {
		return false
	}
	for key := range meta {
		if strings.HasPrefix(key, orchestratorMetadataPrefix) {
			return true
		}
	}
	return false
}

// partitionWorkflowIssues splits issues into orchestrator-managed workflow
// instances and normal issues. The dedup gates (bd duplicates,
// bd find-duplicates) only adjudicate normal issues unless the operator
// explicitly passes --include-workflow.
func partitionWorkflowIssues(issues []*types.Issue) (normal []*types.Issue, workflowCount int) {
	normal = make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if hasOrchestratorMetadata(issue) {
			workflowCount++
			continue
		}
		normal = append(normal, issue)
	}
	return normal, workflowCount
}
