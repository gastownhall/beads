"""The nogo analyzer set: what `go vet` and golangci-lint checked, as go/analysis passes.

This is the single list of analyzers `//tools/nogo` runs on every Go compile:

  - VET_PASSES: the vet checks `go test` runs (cmd/go's defaultVetFlags), which
    pr.yml ran over ./... as `go vet` (scripts/ci/go-test-vet.sh) because
    rules_go's go_test runs none. scripts/pr_lanes_bazel_coverage_test.go keeps
    the list equal to the Go toolchain's. They see every file, tests included,
    as `go vet ./...` did.
  - GOLANGCI_LINTERS: the linters .golangci.yml enables, each wrapped by
    //tools/nogo/internal/golangci so that .golangci.yml's settings,
    run.tests, generated-file handling and exclusion rules apply as they did
    under golangci-lint. //tools/nogo:nogo_test keeps the list equal to
    .golangci.yml's linters.enable.
"""

# cmd/go's defaultVetFlags (Go 1.26), as analyzer names (-bool is "bools",
# -buildtags "buildtag").
VET_PASSES = [
    "atomic",
    "bools",
    "buildtag",
    "directive",
    "errorsas",
    "ifaceassert",
    "nilfunc",
    "printf",
    "slog",
    "stringintconv",
    "tests",
]

GOLANGCI_LINTERS = [
    "depguard",
    "errcheck",
    "forbidigo",
    "gosec",
    "misspell",
    "sloglint",
    "unconvert",
    "unparam",
]

NOGO_ANALYZERS = ["@org_golang_x_tools//go/analysis/passes/" + p for p in VET_PASSES] + [
    "//tools/nogo/analyzers/" + linter
    for linter in GOLANGCI_LINTERS
]
