package dream

import (
	"encoding/json"
	"fmt"
)

// systemPrompt instructs the model on how to consolidate memories conservatively.
const systemPrompt = `You are performing a "dream" pass over an engineer's persistent memory store, similar to how human REM sleep consolidates short-term recall into long-term storage.

Your job: identify (1) duplicate or heavily overlapping memories, (2) memories with stale facts (dates that have passed, removed code paths, abandoned projects), and (3) low-signal entries that add noise. Propose operations to consolidate them.

CRITICAL RULES — read carefully:

1. Be CONSERVATIVE. When in doubt, leave a memory alone. Each memory cost the engineer effort to create. A wrong "forget" is destructive; a missed consolidation is harmless.

2. NEVER touch memories whose key prefix or content describes the user themselves (their role, expertise, identity). These are foundational context.

3. NEVER touch reference-style memories (file paths, URLs, IDs, command snippets) unless the path or ID is verifiably abandoned in the content of another memory.

4. MERGE only when 2 or more memories cover the SAME concept and a single richer entry would not lose information. Pick a clear new_key; produce new_content that subsumes all absorbed entries.

5. UPDATE only when a memory has an obvious factual error AND the corrected content can be derived from other memories or explicit dates in the content.

6. FORGET only when a memory is clearly redundant (covered better by another) OR clearly stale (refers to deleted code/abandoned project that another memory confirms is gone).

7. If you are not confident about an operation, OMIT it. There is no penalty for a small plan; there is real harm in a bad operation.

Call the consolidate_memories tool exactly once with the full operation list and a one-line summary.`

// toolName is the tool the model must call to return its consolidation plan.
const toolName = "consolidate_memories"

// toolDescription describes the consolidation tool to the model.
const toolDescription = "Apply a set of consolidation operations (forget, merge, update) to the memory store. Operations are applied in order. Memories not referenced by any operation are kept as-is."

// toolInputSchema returns the JSON Schema for the consolidate_memories tool input.
// The schema is generated programmatically rather than hardcoded so it stays in
// lockstep with the Plan struct.
func toolInputSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"operations", "summary"},
		"properties": map[string]any{
			"operations": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"action", "reason"},
					"properties": map[string]any{
						"action": map[string]any{
							"type":        "string",
							"enum":        []string{"forget", "merge", "update"},
							"description": "Operation kind. forget: delete by key. merge: replace absorbed_keys with one new entry under new_key. update: rewrite content of an existing key.",
						},
						"key": map[string]any{
							"type":        "string",
							"description": "Target key for forget and update operations.",
						},
						"new_key": map[string]any{
							"type":        "string",
							"description": "Key for the new merged entry. Required for merge.",
						},
						"new_content": map[string]any{
							"type":        "string",
							"description": "New content for merge or update.",
						},
						"absorbed_keys": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Keys to delete after the merged entry is written. Required for merge (>= 2).",
						},
						"reason": map[string]any{
							"type":        "string",
							"description": "One sentence explaining why this operation is safe.",
						},
					},
				},
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "One-line human-readable summary of the plan, e.g. 'merged 2, forgot 1, kept 8'.",
			},
		},
	}
}

// renderUserMessage formats the memory store as a JSON object for the model.
func renderUserMessage(memories []Memory) (string, error) {
	m := make(map[string]string, len(memories))
	for _, mem := range memories {
		m[mem.Key] = mem.Content
	}
	enc, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling memories: %w", err)
	}
	return fmt.Sprintf("Memory store contents (JSON):\n\n%s\n\nApply consolidation operations following the rules above.", string(enc)), nil
}
