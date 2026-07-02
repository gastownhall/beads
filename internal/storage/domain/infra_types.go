package domain

import "github.com/steveyegge/beads/internal/types"

var defaultInfraTypes = []string{"agent", "role", "message"}

var defaultInfraSet = func() map[string]bool {
	m := make(map[string]bool, len(defaultInfraTypes))
	for _, t := range defaultInfraTypes {
		m[t] = true
	}
	return m
}()

func DefaultInfraTypes() []string {
	out := make([]string, len(defaultInfraTypes))
	copy(out, defaultInfraTypes)
	return out
}

func IsInfraType(t types.IssueType) bool {
	return defaultInfraSet[string(t)]
}
