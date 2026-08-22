// Package bench is an in-tree A/B harness for comparing candidate SQL
// shapes for sqlbuild.SearchCountsSQL against a seeded corpus, without
// requiring an external benchmarking rig or a second git checkout. See
// be-qm6fb: PR 5339 changed SearchCountsSQL's predicate form and was
// approved on result-equivalence reasoning alone, then measured ~3x slower
// after merge. This package is the measuring instrument any future attempt
// at that optimization (join-form bounds, temp-table materialization,
// labels-only bound) must clear before review.
//
// Run it with (requires the pinned Dolt image, dolthub/dolt-sql-server:2.2.0,
// cached locally — it skips cleanly otherwise):
//
//	go test -run TestSearchCountsHarness -v ./internal/storage/sqlbuild/bench/...
//
// Run before proposing any SearchCountsSQL shape change; paste the printed
// per-round timings, spread, and EXPLAIN reference counts into the PR
// description.
package bench

import "github.com/steveyegge/beads/internal/storage/sqlbuild"

// ShapeFunc renders a candidate SQL shape for the counts mega-query. It
// mirrors sqlbuild.SearchCountsSQL's signature exactly so any candidate can
// be swapped in without touching call sites.
type ShapeFunc func(tables sqlbuild.FilterTables, ids []string, whereSQL, orderBySQL, limitSQL string, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) (string, []any)

// Shape names a renderer so harness output can report which shape a
// measurement or row set came from.
type Shape struct {
	Name   string
	Render ShapeFunc
}

// MainShape wraps the production sqlbuild.SearchCountsSQL renderer, giving
// the harness a baseline shape to compare candidates against (or, via a
// null self-check, to compare against itself).
func MainShape() Shape {
	return Shape{Name: "main", Render: sqlbuild.SearchCountsSQL}
}
