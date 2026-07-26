# Harness notes

The Rust sources under `src/` are a self-contained conformance harness for `bd`:

- `src/differential.rs` — the scenario runner, `normalize()` (volatile-value
  collapsing), and the JSON-aware `diff()`.
- `src/scenarios.rs` — the curated scenario set (`all()`, always run) plus
  `catalog()`, which loads the enumerated deep-tier set from
  `scenarios/enumerated.json` (~500 deterministic scenarios covering the wider bd
  CLI surface) when `ORACLE_CATALOG` is set. Non-deterministic and
  already-curated entries are filtered out automatically.
- `scenarios/enumerated.json` — the committed deep-tier catalog data (name /
  prefix / steps / deterministic per entry; `notes`/`pins` are documentation the
  runner ignores).
- `src/bin/capture_golden.rs` — records one `<scenario>.trace.json` per scenario
  from the reference `bd` (`ORACLE_REFERENCE_BD`).
- `src/bin/scoreboard.rs` — replays every scenario against the candidate `bd`
  (`ORACLE_CANDIDATE`), diffs each against its golden, and prints the in-scope
  pass/fail scoreboard.

Licensed MIT OR Apache-2.0.

## How goldens are handled

`capture_golden` and `scoreboard` resolve the golden directory to
`CARGO_MANIFEST_DIR/testdata/golden`, i.e. `tests/oracle-a/harness/testdata/golden/`.
That directory is git-ignored and **regenerated on every run** from a freshly
built reference `bd` (see `../run-oracle-a.sh`), so the gate always diffs against
the current "before" rather than a checked-in snapshot.

## Environment contract

The scenario runner scrubs the environment to an explicit whitelist
(`PATH`/`HOME`/`TMPDIR` + `BEADS_TEST_MODE=1`) via `env_clear()` rather than
inheriting the host environment, so a green certifies the same configuration on
every host and run. The `BEADS_TEST_MODE=1` store-construction delta is a
documented ungated gap (see `../README.md`).

## Cargo manifest

`Cargo.toml` is standalone: concrete dependency versions (`serde`, `serde_json`,
`tempfile`, `regex`) and no workspace, so the crate builds on its own with a plain
`cargo build --release --bins`. `Cargo.lock` is committed to keep the two `[[bin]]`
targets reproducible.
