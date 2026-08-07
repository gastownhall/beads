package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
)

func generateRollingCensus(ctx context.Context, releases catalog, result *census, cache string) error {
	sqliteLineage, sqliteFamilies, err := generateRollingSQLiteLineage(ctx, releases, *result, cache)
	if err != nil {
		return fmt.Errorf("generate rolling SQLite census: %w", err)
	}
	doltLineage, doltFamilies, err := generateRollingDoltLineage(ctx, releases, *result, cache)
	if err != nil {
		return fmt.Errorf("generate rolling Dolt census: %w", err)
	}
	return mergeRollingLineages(
		result,
		[]lineageSet{sqliteLineage, doltLineage},
		[][]family{sqliteFamilies, doltFamilies},
	)
}

func mergeRollingLineages(result *census, lineages []lineageSet, familySets [][]family) error {
	if result == nil {
		return errors.New("rolling census result is nil")
	}
	byID := make(map[string]family, len(result.Families))
	for _, candidate := range result.Families {
		byID[candidate.ID] = candidate
	}
	for _, families := range familySets {
		for _, candidate := range families {
			if existing, ok := byID[candidate.ID]; ok {
				if existing.Mode != candidate.Mode || !bytes.Equal(existing.Layout, candidate.Layout) {
					return fmt.Errorf("rolling family %s conflicts with its existing definition", candidate.ID)
				}
				continue
			}
			byID[candidate.ID] = candidate
			result.Families = append(result.Families, candidate)
		}
	}
	for _, lineage := range lineages {
		if lineage.SchemaVersion != lineageSchemaVersion {
			return errors.New("rolling lineage schema version is invalid")
		}
		result.Transitions = append(result.Transitions, lineage.Transitions...)
		result.Outcomes = append(result.Outcomes, lineage.Outcomes...)
	}
	sort.Slice(result.Families, func(i, j int) bool { return result.Families[i].ID < result.Families[j].ID })
	sortLineageTransitions(result.Transitions)
	sortLineageOutcomes(result.Outcomes)
	return nil
}
