package storage

import "github.com/steveyegge/beads/internal/types"

// defaultInfraTypes are the built-in infrastructure types routed to the wisps table.
// Override via DB config "types.infra" or config.yaml types.infra.
var defaultInfraTypes = []string{"agent", "rig", "role", "message"}

// defaultInfraSet is the set form of defaultInfraTypes for IsInfraType lookups.
var defaultInfraSet = func() map[string]bool {
	m := make(map[string]bool, len(defaultInfraTypes))
	for _, t := range defaultInfraTypes {
		m[t] = true
	}
	return m
}()

// DefaultInfraTypes returns a copy of the built-in infrastructure types.
func DefaultInfraTypes() []string {
	out := make([]string, len(defaultInfraTypes))
	copy(out, defaultInfraTypes)
	return out
}

// IsInfraType returns true if the issue type is infrastructure.
// Uses the hardcoded defaults (agent, rig, role, message).
// Prefer DoltStorage.IsInfraTypeCtx when a store is available for config-driven behavior.
func IsInfraType(t types.IssueType) bool {
	return defaultInfraSet[string(t)]
}
