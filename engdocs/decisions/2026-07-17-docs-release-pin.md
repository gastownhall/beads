# Decision record: the docs describe the pinned release, not main

Date: 2026-07-17
Decided by: Chris Sells
Status: settled — do not relitigate without new information

Scope: which bd version the user docs (`docs/`) — hand-written pages and the
generated CLI reference — are written against and validated against.

## The problem

The Mintlify site publishes from `main`, but users run the latest release.
Between a release tag and the next release, `main` accumulates commands,
flags, and behavior no released binary has; docs generated from `main`'s
source then document features users cannot run (as happened between v1.1.0
and this decision: storage backends, pool claiming, `bd migrate --force`,
four unreleased commands).

## Decisions

### 1. The published docs describe the latest release

Hand-written pages state what the released binary does. The docs homepage
names the release it documents and links its release notes. Features that
exist only on `main` are not documented — no "coming soon" warnings, no
future-feature pages.

### 2. The generated CLI reference is pinned via docs/cli-docs.pin

`docs/cli-docs.pin` names the release tag. The docs pipeline
(`scripts/generate-cli-docs.sh`, `scripts/check-doc-flags.sh`,
`scripts/check-cli-docs-drift.sh`) builds bd from that tag
(`scripts/resolve-docs-bd.sh`, CGO_ENABLED=0 `-tags gms_pure_go` — CI's
canonical build) and generates/validates the committed docs against it. A
binary supplied on the command line is ignored while the pin is set
(`BD_DOCS_IGNORE_PIN=1` bypasses, e.g. to preview an unreleased emitter).
The pin is bumped as part of each release, followed by
`./scripts/generate-cli-docs.sh`.

Why: the drift gates stay (docs remain regenerable and hand-edit-proof), but
they now enforce the policy in decision 1 instead of docs-match-main.

### 3. Legacy-emitter releases are bridged in docsmint

Releases up to v1.1.0 emit the CLI reference in the pre-overhaul Docusaurus
form (`website/docs/cli-reference/`). `tools/docsmint` detects that layout
and converts it to the generic staging form before its normal Mintlify
transform (`tools/docsmint/legacy.go`). The shim is removable once the pin
reaches a release whose emitter writes `build/cli-docs/` natively.

### 4. Hidden escape hatches are not documented

`BD_ALLOW_REMOTE_MIGRATE` (and equivalents added later, e.g. a `--force`
migrate flag) stay out of the user docs. The remote-migrate gate prints its
own operator guidance by design; docs defer to it rather than teaching
agents a ready-to-run bypass.
