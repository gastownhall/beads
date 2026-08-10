package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// createDependencyRequests translates parsed --deps specs into the create
// request's edge list. The specs are already alias-normalized and deduped by
// parseDepSpecs; the facade rejects the exact duplicates that dedupe removes.
func createDependencyRequests(specs []domain.DependencySpec) []issueops.CreateDependency {
	if len(specs) == 0 {
		return nil
	}
	requests := make([]issueops.CreateDependency, 0, len(specs))
	for _, spec := range specs {
		requests = append(requests, issueops.CreateDependency{
			TargetID: spec.TargetID,
			Type:     spec.Type,
			Reverse:  spec.SwapDirection,
			Metadata: spec.Metadata,
			ThreadID: spec.ThreadID,
		})
	}
	return requests
}

// waitsForRequest translates the parsed --waits-for spec into the create
// request's spawner gate.
func waitsForRequest(spec *domain.WaitsForSpec) *issueops.WaitsFor {
	if spec == nil {
		return nil
	}
	return &issueops.WaitsFor{SpawnerID: spec.SpawnerID, Gate: spec.Gate}
}

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

// resolveDepSpecTargets rewrites each non-external TargetID through the same
// partial-ID resolution path as `bd dep add` (utils.ResolvePartialID).
//
// Without this, `bd create --deps discovered-from:8vezf` stores the bare
// token as depends_on_id, producing a dangling edge that `bd dep list` cannot
// see and that `bd dep remove … 8vezf` then mis-targets after resolving the
// good fully-qualified row (GH#5005).
func resolveDepSpecTargets(ctx context.Context, st storage.Storage, specs []domain.DependencySpec) ([]domain.DependencySpec, error) {
	if len(specs) == 0 {
		return specs, nil
	}
	out := make([]domain.DependencySpec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		target := strings.TrimSpace(spec.TargetID)
		if target == "" {
			return nil, fmt.Errorf("--deps target is empty")
		}
		if strings.HasPrefix(target, "external:") {
			out[i].TargetID = target
			continue
		}
		resolved, err := utils.ResolvePartialID(ctx, st, target)
		if err != nil {
			return nil, fmt.Errorf("resolving --deps target %q: %w", target, err)
		}
		out[i].TargetID = resolved
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

	spec := domain.DependencySpec{TargetID: target, Type: canonicalDependencyType(rawType)}
	if rawType == types.DepBlocks {
		// Explicit "blocks:" (as opposed to the "depends-on"/"blocked-by"
		// aliases, which keep direction) reverses direction: the target
		// depends on the issue being created, not the other way around.
		spec.SwapDirection = true
	}

	if err := validateDependencyType(spec.Type); err != nil {
		return domain.DependencySpec{}, err
	}
	return spec, nil
}

// canonicalDependencyType maps the documented alias spellings "depends-on"
// and "blocked-by" to the canonical "blocks" storage type that ready/blocked
// gating checks (types.DependencyType.AffectsReadyWork). Both `bd create
// --deps` and `bd dep add --type` share this mapping so an alias always
// gates the same way as the canonical type — see GH#5069, where `bd dep add
// --type blocked-by` stored the literal string "blocked-by" and silently
// never gated `bd ready`/`bd blocked`, even though `--help` itself documents
// "blocked-by" as the flag name. Any other value (including well-known types
// like "parent-child" and custom types) passes through unchanged.
func canonicalDependencyType(t types.DependencyType) types.DependencyType {
	switch t {
	case "depends-on", "blocked-by":
		return types.DepBlocks
	default:
		return t
	}
}

// validateDependencyType enforces that a (post-alias-normalization)
// dependency type is both structurally valid and one of the well-known
// built-in types. `bd create --deps` and `bd dep add --type` intentionally
// reject custom/unknown dependency types (see the comment on
// WellKnownDependencyTypes) — this is the single shared check so both
// commands stay in lockstep.
func validateDependencyType(t types.DependencyType) error {
	if !t.IsValid() {
		return fmt.Errorf("invalid dependency type %q (must be non-empty, max %d chars); valid types: %s",
			t, types.MaxDependencyTypeLen, createDepsAcceptedTypeList())
	}
	if !t.IsWellKnown() {
		return fmt.Errorf("unknown dependency type %q; valid types: %s",
			t, createDepsAcceptedTypeList())
	}
	return nil
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
