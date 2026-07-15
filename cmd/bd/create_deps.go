package main

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func parseDepSpecs(deps []string) ([]domain.DependencySpec, error) {
	var out []domain.DependencySpec
	for _, raw := range expandDepFlagValues(deps) {
		spec, err := parseDepSpec(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	if err := checkDepSpecsUniqueTargets(out); err != nil {
		return nil, err
	}
	return out, nil
}

// expandDepFlagValues flattens comma-separated --deps tokens.
// Cobra StringSlice already splits on commas, but a single token may still
// contain "type:id,type:id" when passed as one shell word without slice split.
func expandDepFlagValues(deps []string) []string {
	var out []string
	for _, raw := range deps {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out = append(out, part)
		}
	}
	return out
}

// checkDepSpecsUniqueTargets rejects multi-type edges that would collide on
// uk_dep_issue_target (unique per (issue_id, target), type not part of the key).
// GH#4626: discovered-from:X,blocked-by:X used to silently keep only one edge.
func checkDepSpecsUniqueTargets(specs []domain.DependencySpec) error {
	// Key: swapDirection|target — same effective endpoint pair for a new issue.
	seen := make(map[string]types.DependencyType, len(specs))
	for _, s := range specs {
		key := fmt.Sprintf("%t|%s", s.SwapDirection, s.TargetID)
		if prev, ok := seen[key]; ok && prev != s.Type {
			return fmt.Errorf(
				"--deps cannot attach both %q and %q to the same target %q: dependency uniqueness is per target id, not per type (uk_dep_issue_target). Pick one type, or open a separate issue for the second relationship (GH#4626)",
				prev, s.Type, s.TargetID,
			)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("--deps lists the same dependency on %q more than once", s.TargetID)
		}
		seen[key] = s.Type
	}
	return nil
}

func parseDepSpec(raw string) (domain.DependencySpec, error) {
	if !strings.Contains(raw, ":") {
		return domain.DependencySpec{
			Type:     types.DepBlocks,
			TargetID: raw,
		}, nil
	}

	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return domain.DependencySpec{}, fmt.Errorf("invalid dependency format %q, expected 'type:id' or 'id'", raw)
	}
	rawType := types.DependencyType(strings.TrimSpace(parts[0]))
	target := strings.TrimSpace(parts[1])

	spec := domain.DependencySpec{TargetID: target}
	switch rawType {
	case "depends-on", "blocked-by":
		spec.Type = types.DepBlocks
	case types.DepBlocks:
		spec.Type = types.DepBlocks
		spec.SwapDirection = true
	default:
		spec.Type = rawType
	}

	if !spec.Type.IsValid() {
		return domain.DependencySpec{}, fmt.Errorf("invalid dependency type %q (must be non-empty, max 50 chars); valid types: %s",
			spec.Type, createDepsAcceptedTypeList())
	}
	if !spec.Type.IsWellKnown() {
		return domain.DependencySpec{}, fmt.Errorf("unknown dependency type %q; valid types: %s",
			spec.Type, createDepsAcceptedTypeList())
	}
	return spec, nil
}

func buildWaitsFor(spawnerID, gate string) (*domain.WaitsForSpec, error) {
	spawnerID = strings.TrimSpace(spawnerID)
	if spawnerID == "" {
		return nil, nil
	}
	if gate == "" {
		gate = types.WaitsForAllChildren
	}
	if gate != types.WaitsForAllChildren && gate != types.WaitsForAnyChildren {
		return nil, fmt.Errorf("invalid --waits-for-gate value %q (valid: all-children, any-children)", gate)
	}
	return &domain.WaitsForSpec{SpawnerID: spawnerID, Gate: gate}, nil
}

func discoveredFromParent(deps []string) string {
	for _, raw := range deps {
		raw = strings.TrimSpace(raw)
		if raw == "" || !strings.Contains(raw, ":") {
			continue
		}
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 {
			continue
		}
		depType := types.DependencyType(strings.TrimSpace(parts[0]))
		target := strings.TrimSpace(parts[1])
		if depType == types.DepDiscoveredFrom && target != "" {
			return target
		}
	}
	return ""
}

func overlayYAMLPrefix(dbPrefix string) string {
	if v := strings.TrimSpace(config.GetString("issue-prefix")); v != "" {
		return v
	}
	return dbPrefix
}
