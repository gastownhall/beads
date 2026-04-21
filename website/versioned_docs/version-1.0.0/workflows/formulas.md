---
id: formulas
title: Formulas
sidebar_position: 3
---

# Formulas

Formulas are declarative workflow templates.

## Formula Format

Formulas can be written in TOML (preferred) or JSON:

### TOML Format

```toml
formula = "feature-workflow"
description = "Standard feature development workflow"
version = 1
type = "workflow"

[vars.feature_name]
description = "Name of the feature"
required = true

[[steps]]
id = "design"
title = "Design {{feature_name}}"
type = "human"
description = "Create design document"

[[steps]]
id = "implement"
title = "Implement {{feature_name}}"
needs = ["design"]

[[steps]]
id = "review"
title = "Code review"
needs = ["implement"]
type = "human"

[[steps]]
id = "merge"
title = "Merge to main"
needs = ["review"]
```

### JSON Format

```json
{
  "formula": "feature-workflow",
  "description": "Standard feature development workflow",
  "version": 1,
  "type": "workflow",
  "vars": {
    "feature_name": {
      "description": "Name of the feature",
      "required": true
    }
  },
  "steps": [
    {
      "id": "design",
      "title": "Design {{feature_name}}",
      "type": "human"
    },
    {
      "id": "implement",
      "title": "Implement {{feature_name}}",
      "needs": ["design"]
    }
  ]
}
```

## Formula Types

| Type | Description |
|------|-------------|
| `workflow` | Standard step sequence |
| `expansion` | Template for expansion operator |
| `aspect` | Cross-cutting concerns |
| `convoy` | Multi-agent coordination (parallel workers) |

### Convoy Formulas

Convoy formulas coordinate multiple agents working in parallel. Use them for code review with multiple reviewers, design review sessions, or any workflow requiring parallel human or agent coordination.

```toml
formula = "multi-reviewer"
description = "Parallel code review with multiple reviewers"
version = 1
type = "convoy"

[vars.reviewers]
description = "Comma-separated reviewer names"
required = true

[[steps]]
id = "review-setup"
title = "Prepare review artifacts"

[[steps]]
id = "collect"
title = "Collect reviews"
needs = ["review-setup"]
waits_for = "all-children"
```

## Variables

Define variables with defaults and constraints:

```toml
[vars.version]
description = "Release version"
required = true
pattern = "^\\d+\\.\\d+\\.\\d+$"

[vars.environment]
description = "Target environment"
default = "staging"
enum = ["staging", "production"]
```

Use variables in steps:

```toml
[[steps]]
title = "Deploy {{version}} to {{environment}}"
```

## Step Types

| Type | Description |
|------|-------------|
| `task` | Normal work step (default) |
| `human` | Requires human action |
| `gate` | Async coordination point |

## Dependencies

### Sequential

```toml
[[steps]]
id = "step1"
title = "First step"

[[steps]]
id = "step2"
title = "Second step"
needs = ["step1"]
```

### Parallel then Join

```toml
[[steps]]
id = "test-unit"
title = "Unit tests"

[[steps]]
id = "test-integration"
title = "Integration tests"

[[steps]]
id = "deploy"
title = "Deploy"
needs = ["test-unit", "test-integration"]  # Waits for both
```

## Conditional Steps

Steps can be made conditional based on a variable:

```toml
[vars.run_security_scan]
description = "Whether to run security scan"
default = "true"

[[steps]]
id = "security-scan"
title = "Run security scan"
condition = "{{run_security_scan}}"

[[steps]]
id = "deploy"
title = "Deploy to production"
condition = "{{environment}} == production"
```

Condition formats:
- `"{{var}}"` - truthy (non-empty, non-false)
- `"!{{var}}"` - negated
- `"{{var}} == value"` - equality
- `"{{var}} != value"` - inequality

Conditions are evaluated at cook/pour time. Steps that don't match are removed from the workflow.

## Loops and Iteration

Steps can iterate using the `loop` field:

### Fixed Count

```toml
[[steps]]
id = "retry"
title = "Attempt {{i}}"

[steps.loop]
count = 3
var = "i"

[[steps.loop.body]]
id = "try-{{i}}"
title = "Attempt {{i}}"
```

### Range

```toml
[[steps]]
id = "phases"
title = "Phase iteration"

[steps.loop]
range = "1..5"
var = "phase_num"

[[steps.loop.body]]
id = "phase-{{phase_num}}"
title = "Execute phase {{phase_num}}"
```

Range supports expressions: `"1..2^{disks}"`, `"{start}..{count}"`.

### Until (Conditional)

```toml
[steps.loop]
until = "step.status == 'complete'"
max = 10
var = "attempt"

[[steps.loop.body]]
id = "attempt-{{attempt}}"
title = "Attempt {{attempt}}"
```

The `max` field is required for `until` loops to prevent unbounded iteration.

## Runtime Expansion (on_complete)

Steps can trigger dynamic work when they complete using `on_complete` with `for_each`:

```toml
[[steps]]
id = "survey"
title = "Survey available workers"

[steps.on_complete]
for_each = "output.workers"
bond = "worker-arm"
parallel = true

[steps.on_complete.vars]
worker_name = "{item.name}"
rig = "{item.rig}"
```

This creates a new molecule from the `worker-arm` formula for each item in the `output.workers` array. Use `sequential = true` instead of `parallel` to run them one at a time.

## Formula Inheritance

Formulas can inherit from parent formulas using `extends`:

```toml
formula = "secure-release"
description = "Release with security audit"
version = 1
type = "workflow"
extends = ["release"]

# Inherits all vars and steps from "release"
# Add new steps or override existing ones by ID:
[[steps]]
id = "security-audit"
title = "Security audit"
needs = ["test"]

# Override parent step
[[steps]]
id = "publish"
title = "Publish release (with audit)"
needs = ["tag", "security-audit"]
```

Child formulas inherit all vars, steps, and compose rules from parents. Child definitions with the same ID override parent definitions.

## Cooking Formulas

`bd cook` compiles a formula into a resolved proto. Two modes:

### Compile-time (default)

Produces a proto with `{{variable}}` placeholders intact. Useful for modeling, estimation, and planning.

```bash
bd cook feature-workflow                    # Template with placeholders
bd cook feature-workflow --dry-run          # Preview steps
```

### Runtime

Produces a fully-resolved proto with variables substituted. Requires all variables to have values.

```bash
bd cook feature-workflow --var feature_name=auth      # Substitute vars
bd cook feature-workflow --mode=runtime --var name=x  # Explicit runtime
```

For most workflows, prefer using `bd pour` or `bd mol wisp` directly - they cook the formula inline.

## Gates

Add gates for async coordination:

```toml
[[steps]]
id = "approval"
title = "Manager approval"
type = "human"

[steps.gate]
type = "human"
approvers = ["manager"]

[[steps]]
id = "deploy"
title = "Deploy to production"
needs = ["approval"]
```

Gate types: `human`, `timer`, `gh:run` (GitHub Actions), `gh:pr` (pull request).

## Aspects (Cross-cutting)

Apply transformations to matching steps:

```toml
formula = "security-scan"
type = "aspect"

[[advice]]
target = "*.deploy"  # Match all deploy steps

[advice.before]
id = "security-scan-{step.id}"
title = "Security scan before {step.title}"
```

Aspects support `before`, `after`, and `around` advice. Apply aspects to a formula via `compose.aspects`:

```toml
[compose]
aspects = ["security-scan", "logging"]
```

## Formula Locations

Formulas are searched in order:
1. `.beads/formulas/` (project-level)
2. `~/.beads/formulas/` (user-level)

## Using Formulas

```bash
# List available formulas
bd formula list

# Cook (compile) a formula
bd cook <formula-name> [--var key=value]

# Pour formula into molecule (persistent)
bd mol pour <formula-name> --var key=value

# Create wisp from formula (ephemeral)
bd mol wisp <formula-name> --var key=value

# Preview what would be created
bd mol pour <formula-name> --dry-run
```

## Creating Custom Formulas

1. Create file: `.beads/formulas/my-workflow.formula.toml`
2. Define structure (see examples above)
3. Use with: `bd mol pour my-workflow`

## Example: Release Formula

```toml
formula = "release"
description = "Standard release workflow"
version = 1

[vars.version]
required = true
pattern = "^\\d+\\.\\d+\\.\\d+$"

[[steps]]
id = "bump-version"
title = "Bump version to {{version}}"

[[steps]]
id = "changelog"
title = "Update CHANGELOG"
needs = ["bump-version"]

[[steps]]
id = "test"
title = "Run full test suite"
needs = ["changelog"]

[[steps]]
id = "build"
title = "Build release artifacts"
needs = ["test"]

[[steps]]
id = "tag"
title = "Create git tag v{{version}}"
needs = ["build"]

[[steps]]
id = "publish"
title = "Publish release"
needs = ["tag"]
type = "human"
```
