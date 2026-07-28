package main

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func parseDepSpecs(deps []string) ([]domain.DependencySpec, error) {
	// deps arrives already comma-split: cobra's StringSlice flag CSV-decodes
	// each --deps value, so re-splitting on "," here would double-decode a
	// CSV-quoted target that legitimately contains a comma. Only trim and
	// drop empties; splitting is cobra's job.
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
	return dedupeDepSpecs(out)
}

// dedupeDepSpecs collapses repeated identical edges and rejects edges that
// would collide on the (issue_id, target) dependency-uniqueness key with a
// *different* type. Type is not part of that key, so two different types on
// the same target can't both be stored — but the storage layer already
// treats a repeated identical (target, type) add as idempotent, so an exact
// repeat here must be deduped rather than rejected.
// GH#4626: discovered-from:X,blocked-by:X used to silently keep only one edge.
func dedupeDepSpecs(specs []domain.DependencySpec) ([]domain.DependencySpec, error) {
	// Key: swapDirection|target — same effective endpoint pair for a new issue.
	seen := make(map[string]types.DependencyType, len(specs))
	out := make([]domain.DependencySpec, 0, len(specs))
	for _, s := range specs {
		key := fmt.Sprintf("%t|%s", s.SwapDirection, s.TargetID)
		prev, ok := seen[key]
		switch {
		case !ok:
			seen[key] = s.Type
			out = append(out, s)
		case prev != s.Type:
			return nil, fmt.Errorf(
				"--deps cannot attach both %q and %q to the same target %q: a target can only carry one dependency type at a time. Pick one type, or open a separate issue for the second relationship (GH#4626)",
				prev, s.Type, s.TargetID,
			)
		default:
			// Identical edge repeated (e.g. blocked-by and depends-on both
			// normalize to the same type/target) — silently dedupe.
		}
	}
	if len(out) == 0 {
		return nil, nil
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

// buildWaitsFor validates and constructs a WaitsForSpec from the --waits-for
// and --waits-for-gate flag values. gateExplicit must be true when the caller
// explicitly passed --waits-for-gate (not relying on its default); in that case
// a missing spawnerID is rejected rather than silently ignored.
func buildWaitsFor(spawnerID, gate string, gateExplicit bool) (*domain.WaitsForSpec, error) {
	spawnerID = strings.TrimSpace(spawnerID)
	if spawnerID == "" {
		if gateExplicit {
			return nil, fmt.Errorf("--waits-for-gate requires --waits-for (no spawner ID specified)")
		}
		return nil, nil
	}
	if gate == "" {
		gate = types.WaitsForAllChildren
	}
	if !types.IsValidWaitsForGate(gate) {
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

// discoveredFromParentSpec is discoveredFromParent's counterpart for callers
// that already ran deps through parseDepSpecs. It reuses the canonical
// parsed/normalized specs instead of re-deriving type:target parsing from
// raw strings a second time, so it can't drift from parseDepSpec's rules
// (e.g. the depends-on/blocked-by aliasing).
func discoveredFromParentSpec(specs []domain.DependencySpec) string {
	for _, s := range specs {
		if s.Type == types.DepDiscoveredFrom && s.TargetID != "" {
			return s.TargetID
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
