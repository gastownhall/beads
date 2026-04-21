# Starter Formulas

Example `.formula.toml` files you can use as starting points.

## Usage

Copy formulas to your project or user-level directory:

```bash
# Project-level (available in this project only)
cp *.formula.toml /path/to/project/.beads/formulas/

# User-level (available in all projects)
cp *.formula.toml ~/.beads/formulas/
```

Then list and use them:

```bash
bd formula list        # See available formulas
bd mol pour release --var version=1.2.0   # Pour into a molecule
```

## Included Formulas

| Formula | Description | Use as |
|---------|-------------|--------|
| `feature-workflow` | Design, implement, review, merge | Molecule (persistent) |
| `release` | Bump version, test, tag, publish | Molecule (persistent) |
| `quick-check` | Lint, test, build sanity check | Wisp (ephemeral) |

## Creating Your Own

See the [Formulas documentation](https://gastownhall.github.io/beads/docs/workflows/formulas) for the full reference.
