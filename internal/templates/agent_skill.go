// Package templates provides embedded files that bd writes into user workspaces.
package templates

import _ "embed"

//go:embed skills/beads/SKILL.md
var beadsAgentSkill string

//go:embed skills/beads/agents/openai.yaml
var beadsAgentSkillOpenAIYAML string

// BeadsAgentSkill returns the repo-local Beads agent skill content.
func BeadsAgentSkill() string {
	return beadsAgentSkill
}

// BeadsAgentSkillOpenAIYAML returns the OpenAI UI metadata for the Beads agent skill.
func BeadsAgentSkillOpenAIYAML() string {
	return beadsAgentSkillOpenAIYAML
}
