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
	for _, raw := range deps {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		spec, err := parseDepSpec(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
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
