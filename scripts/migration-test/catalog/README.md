# Historical release catalog

`../release-catalog.json` pins the exact historical release universe used by the
v1.2 upgrade census: 122 stable module versions authenticated through the Go
proxy and SumDB, each with module / `go.mod` / source-zip / proxy-origin
provenance, explicit proxy prerelease and repository-only tag exclusions, and
the current repository tag drift.

The `catalog` command in this directory both regenerates and validates that
artifact (`usage: catalog generate|validate <manifest.json>`):

- `go run ./scripts/migration-test/catalog validate scripts/migration-test/release-catalog.json`
  runs the strict offline validator: schema checks, semantic validation,
  canonical byte-form, and the pinned SHA-256 identity digest. Even a well-formed
  substitution is rejected until that digest is deliberately updated.
- `go run ./scripts/migration-test/catalog generate scripts/migration-test/release-catalog.json`
  deterministically rebuilds the catalog from the live Go proxy and GitHub
  release APIs (network plus `go`/`gh` required). This is a manual maintenance
  path, not a CI step.

## Status: self-validated reference anchor

Today the catalog is a reviewed-identity anchor. Its only automated enforcement
is `TestCheckedCatalogIsCanonicalAndComplete` in `main_test.go`, which runs
under `go test ./...` and holds the checked JSON to the pinned digest and its
invariants. Nothing in the migration-test harness (`run.sh`,
`historical-dolt-upgrade-test.sh`, `legacy-bridge-test.sh`) consumes it yet.

The intended consumer is the upgrade-coverage work stacked on top of this
change: runtime schema observations and family/path qualification will drive
upgrade tests against this finite, reproducible version universe. Until that
lands, regenerate deliberately with `generate` when upstream history changes —
drift from live upstream is not detected automatically.
