# BDP in beads

BDP, the Beads Protocol, is specified in
[gastownhall/bdp](https://github.com/gastownhall/bdp). `bd serve` and
`bd connect` are to gain it as a second, opt-in protocol behind
`--protocol bdp|both`, marked [EXPERIMENTAL]. `v0` stays the default, and
beads' own HTTP contract stays `/v0`, defined by
`internal/httpapi/spec/openapi.v0.yaml` and
[bd-serve-v0.md](bd-serve-v0.md).

Main has no BDP protocol yet: no `--protocol` flag and no `/bdp` mount,
only the vendored copy of the bdp files BDP's conformance tests will read
and its pin (below). A default `bd serve` answers `/bdp` and the paths under
it with v0's unrouted 404, which `TestDefaultSurfaceAnswersV0sUnrouted404`
(`internal/httpapi/mount_census_test.go`) pins. The bring-up lands in small
slices under [#8319](https://github.com/gastownhall/beads/issues/8319). Each
slice that adds a BDP surface, file or test updates this page in the same
PR.

## For every engineer

Build and test as usual. You don't fetch, update or read the BDP spec.

**A v0 test fails on your change.** These guard beads' own `/v0` promise:

- the default-surface golden, `TestDefaultSurfaceAnswersV0sUnrouted404`;
- the per-operation client wire golden, `TestV0ClientRequestsAreFrozen`
  (`internal/httpclient/v0_wire_golden_test.go`);
- the served corpus's request transcripts (see
  [Wire transcripts](../../backend/http/corpus/README.md#wire-transcripts));
- the wire-shape digest, `TestWireShapeDigest`
  (`internal/httpapi/wireshape`).

The `cmd/bd/protocol` tests guard the CLI's own contracts (`--json` shapes,
exit codes; see [CATALOG.md](../../cmd/bd/protocol/CATALOG.md)). None of
these is a BDP test, and none needs anything from the BDP repo. If you didn't
mean the change, fix your change. If you did, regenerate with the command
the failure prints, review the diff, and say in the PR what changed and why.
A `/v0` change also follows [bd-serve-v0.md](bd-serve-v0.md): a non-additive
change to an existing member bumps `wire_revision` and adds its history
entry in `openapi.v0.yaml`; an additive one needs neither. A `--json` change
follows CATALOG.md (`make corpus-regen`) and
[json-schema.md](../../docs/reference/json-schema.md), which say when it also
bumps `JSONSchemaVersion` in `cmd/bd/output.go`. When BDP's Callables land,
they will take their parameters from `openapi.v0.yaml`. A v0 change
therefore never needs a Callables or spec edit, and a new v0 operation is
simply not exposed over BDP until someone names it.

**A BDP test fails on your change.** No test of a BDP surface exists on
main yet; `TestVendoredBDPMatchesPin` fails only when `third_party/bdp/` is
edited by hand (see "The pin"). While
`--protocol` is experimental, a beads change may break a BDP surface
outright, but never silently: say so in the PR and file an issue on
gastownhall/bdp. BDP's conformance tests (a later slice) will add the exact
steps here, and to their failure messages, when they land. There is no
waiver file.

Two rules:

- Never hand-edit the vendored BDP copy or the pin, even with a bdp PR open.
  Only `scripts/bdp-vendor.sh` writes them: at a new checkpoint in a
  pin-bump PR (below), or at the same pin in the PR that adds a consumer of
  another bdp file. So the vendored bytes always equal a checkpoint that
  bdp has merged and tagged.
- If the spec looks wrong, file an issue on gastownhall/bdp. Don't code
  around it silently.

**Changing BDP itself.** The spec changes in gastownhall/bdp, on bdp's
schedule. A beads PR changes beads only. It never carries a spec edit and
never waits on a bdp PR. To change the spec, file the issue or open the bdp
PR yourself. Once bdp merges and tags the change, a pin-bump PR brings it
in.

## Releases

Nothing BDP-specific. Releases, release branches and hotfixes follow
[RELEASING.md](../../RELEASING.md) unchanged. Release notes make no
statement about BDP conformance, and no pin bump rides along with a
release.

## Bumping the pin

The pin moves only in a pin-bump PR approved by Julian Knutsen.

- **Target.** An annotated bdp tag under `spec/` (for example
  `spec/sprint1-20261109` or `spec/v0.1`) whose commit is reachable from
  gastownhall/bdp `main`, as `scripts/bdp-vendor.sh` enforces. Never a
  branch tip, a fork, a lightweight tag or an untagged commit.
- **Forward only.** The new commit descends from the current one on bdp
  `main`. Reverting a bump PR, for example to clear a red main, restores the
  previous pin and is not a backward bump.
- **Its own PR.** The pin, the copy re-vendored byte-for-byte from the
  checkpoint, and only the beads changes the new checkpoint requires. A
  change to shared code lands first, in its own PR.
- **specRevision.** The last commit at or before the pinned commit that
  changed `specs/profiles/core/core.md`, `specs/profiles/write/write.md`,
  `specs/profiles/types/types.md` or `specs/profiles/events/events.md`.
  bdp's `scripts/canonical-spec-revision.mjs` computes it the same way: a
  plain `git log -1` over those four files, not `--first-parent`, so it can
  be a merge inside a PR branch. It can differ from the pinned commit. The
  vendored copy has no git history to recompute it, so the pin records it.
- **What changed.** The PR lists the bdp changes between the two checkpoints
  that alter what `bd serve` or `bd connect` sends or accepts.

The spec and its checkpoints are written in gastownhall/bdp. bdp's
TypeScript kit is the reference at pin bumps; the Go conformance tests that
run here are beads' own.

## The pin

The vendored copy lives in `third_party/bdp/spec/`, at bdp's own paths, and
its pin and lock is `third_party/bdp/bdp.pin`. The pin records the
checkpoint the copy was taken from (repository, tag, tag object, commit,
specRevision and the rule that derives it) and, for each vendored file, its
size, sha256 and git blob id. `scripts/bdp-vendor.sh` writes both. bdp's
MIT notice is also listed in `THIRD_PARTY_LICENSES`.

The pin's `lock` line is the sha256 of all its other non-comment lines.
Two PRs that each change `bdp.pin`, such as a consumer slice and a pin
bump, therefore always conflict on that line, even when they touch
different files, and git can never merge them into a pin that mixes two
checkpoints' bytes. The PR that merges second takes main's `bdp.pin`
(`git checkout origin/main -- third_party/bdp/bdp.pin`) and reruns the
script: at main's tag in a consumer slice, at its new tag in a pin bump.
Never resolve a `bdp.pin` conflict by hand.

`TestVendoredBDPMatchesPin` (`third_party/bdp/pin_test.go`) fails when a
file under `spec/` is changed, added or removed, when `bdp.pin` is
malformed, its non-comment lines are not in the script's exact format or
its `lock` line does not match them, or when bdp's `LICENSE` is missing.
It cannot check the checkpoint itself (tag, tag object, commit,
specRevision) without bdp's history, so the reviewer of every PR that
touches `bdp.pin` runs `scripts/bdp-vendor.sh <bdp checkout> <tag> --check`
against a full, freshly fetched clone of gastownhall/bdp.

The copy holds only files with a named consumer: bdp's `LICENSE`, and the
core response schema and two current catalogs that BDP's conformance tests
(a later slice) will read. Until they land, nothing in beads reads the
schema or the catalogs. A slice that adds a consumer of another bdp file
adds the path to the script's list, with the consumer beside it, and reruns
the script at the same pin. bdp's TypeScript, its v0 cumulative matrices,
catalogs, development data and fixtures, and spec prose are not vendored:
nothing in beads runs or reads them, and bdp's own `specs/conformance.md`
says v0.1 inherits no pass from that matrix. The conformance tests' failure
messages will link to the spec at the pinned commit instead.

**Re-pinning.** Run `scripts/bdp-vendor.sh <bdp checkout> <tag>` from a
full (not shallow), freshly fetched clone of gastownhall/bdp, for a new
annotated `spec/*` tag reachable from bdp `main`. It refuses a lightweight
or non-`spec/*` tag, a commit not reachable from the checkout's
`origin/main`, and a commit that does not descend from the current pin.
`scripts/bdp-vendor.sh <bdp checkout> <tag> --check` verifies a tree
without writing. It compares the tree with what `<tag>` generates and does
not check the direction, so the reviewer of a pin bump also runs
`git -C <bdp> merge-base --is-ancestor OLD NEW`, which exits 0 only for a
forward move; OLD is the commit in main's `bdp.pin`. The pin-bump PR shows
the `bdp.pin` diff (the vendored bytes are marked generated, so their diff
collapses) and `git -C <bdp> log --oneline OLD..NEW -- <vendored paths>`.
Julian Knutsen approves it.

The slice that adds `--protocol` will add a test that the specRevision
`bd serve` advertises equals the pin's.
