// Package dream provides AI-powered consolidation for the bd memory store.
//
// A "dream" pass reads all stored memories (bd remember), asks an LLM to
// identify duplicates, stale references, and low-signal entries, and applies
// the resulting plan via bd's existing remember/forget operations.
package dream

// Memory is a single entry in the memory store, identified by a stable key.
type Memory struct {
	Key     string `json:"key"`
	Content string `json:"content"`
}

// Action is the kind of operation the LLM proposes for a consolidation pass.
type Action string

const (
	// ActionForget removes a memory by key.
	ActionForget Action = "forget"
	// ActionMerge replaces multiple memories with a single new one.
	ActionMerge Action = "merge"
	// ActionUpdate rewrites an existing memory's content under the same key.
	ActionUpdate Action = "update"
)

// Operation describes a single change to the memory store.
//
// Required fields by action:
//   - forget: Key
//   - merge:  NewKey, NewContent, AbsorbedKeys (>= 2)
//   - update: Key, NewContent
//
// Reason is required for all actions and surfaces in dry-run output and
// the dream log so the user can audit the LLM's judgment.
type Operation struct {
	Action       Action   `json:"action"`
	Key          string   `json:"key,omitempty"`
	NewKey       string   `json:"new_key,omitempty"`
	NewContent   string   `json:"new_content,omitempty"`
	AbsorbedKeys []string `json:"absorbed_keys,omitempty"`
	Reason       string   `json:"reason"`
}

// Plan is the LLM's proposed consolidation, returned via tool_use.
type Plan struct {
	Operations []Operation `json:"operations"`
	Summary    string      `json:"summary"`
}
