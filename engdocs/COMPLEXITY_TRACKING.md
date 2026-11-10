# Cyclomatic complexity experiment

`scripts/ci/complexity.sh` is an opt-in report for production Go functions.
It uses [`gocyclo`](https://github.com/fzipp/gocyclo) v0.6.0 and reports
functions at or above a threshold (30 by default). The analyzer input is an
explicit shipped-code allowlist: root Go files plus `cmd/`, `internal/`,
`backend/`, `beadserrors/`, `format/`, `issueops/`, `journalops/`,
`memoryops/`, `schema/`, `plugins/`, `integrations/`, and `release-gates/`.
Test files, generated files, and `backend/conformance/` are excluded. Keeping
the scope explicit prevents fixtures, tools, and other non-production trees
from silently changing the signal. The current snapshot is kept in
`engdocs/complexity-baseline.txt` so a function can be compared by package,
name, and file even when line numbers move.

Install the tool once, then run:

```sh
go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0
make ci-complexity             # advisory report
COMPLEXITY_BASE_REF=origin/main make ci-complexity-diff  # PR delta report
make ci-complexity-check       # fail on new or regressed tracked offenders
./scripts/ci/complexity.sh update  # intentionally refresh the snapshot
```

The report target is intentionally separate from required PR checks while the
signal and threshold are evaluated. A future CI guard can promote
`ci-complexity-check` after maintainers agree which existing complexity is
acceptable and how quickly the baseline should be reduced. `diff` compares a
git archive of `COMPLEXITY_BASE_REF` by stable package/function/file keys. It is
file-level attribution, so it is a signal rather than a merge gate.
`COMPLEXITY_TOP`, `COMPLEXITY_THRESHOLD`, and `COMPLEXITY_BASELINE` may be
overridden for local experiments.
