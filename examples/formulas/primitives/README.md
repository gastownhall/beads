# Formula primitive examples

One curated `.formula.toml` per *empirically wired* primitive in
`internal/formula/types.go`. Each fixture is minimal, self-documenting,
and exercises exactly one primitive.

## What's here

| Fixture                       | Primitive          | One-line behavior |
|-------------------------------|--------------------|-------------------|
| `loop-count.formula.toml`     | `LoopSpec.count`   | Body expanded N times into `.iterN.<id>` step IDs. |
| `loop-range.formula.toml`     | `LoopSpec.range`   | Body expanded over a numeric range; `loop.var` substitutes into title/description. |
| `children-epic.formula.toml`  | `Step.children`    | Nested steps form an epic with sibling `needs` honored. |
| `branch-fanin.formula.toml`   | `ComposeRules.branch` | Fork-join: parallel steps fan in to a single join step. |
| `condition.formula.toml`      | `Step.condition`   | Variable-driven step inclusion at cook time. |
| `gate-timer.formula.toml`     | `Step.gate`        | Sibling `gate`-typed issue with `await_type` blocks the target step. |

For the full schema index - every exported struct declared in
`internal/formula/types.go`, with field types and tags -
run:

```sh
bd formula schema                 # list every declared schema struct
bd formula schema loop            # show LoopSpec fields
bd formula primitives gate        # alias
bd formula schema --json          # machine-readable
```

Treat `bd formula schema` as structural reference. Treat this directory
as the verified authoring surface: every fixture here is smoke-tested for
an observable parse-to-cook effect.

## Smoke harness

`cmd/bd/formula_primitives_test.go` walks this directory, parses and
cooks every fixture, and asserts each primitive's observable effect on
the cooked subgraph. A new fixture added here without a registered
assertion is a deliberate test failure: the harness exists to prove
primitives are wired, not just that fixtures parse.

Run with `make test`, or in isolation:

```sh
go test -tags gms_pure_go -run TestFormulaPrimitiveExamples ./cmd/bd/
```

## What's NOT here

* `on_complete` — runtime no-op for every valid configuration as of
  v1.0.3 (tracked in `beads-i02`). Its fixture and smoke test ship with
  that issue's resolution PR, not here.
* Anything in `internal/formula/types.go` without a verified end-to-end
  parse → cook → pour audit. New primitives earn a fixture once they're
  empirically wired, not when they're declared.
