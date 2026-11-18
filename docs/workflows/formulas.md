---
title: Formulas
description: Writing declarative TOML or JSON workflow templates with steps, variables, dependencies, gates, and aspects, then cooking them into protos.
---

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

A step's `type` sets the issue type of the bead it creates: `task`
(default), `bug`, `feature`, `epic`, `chore`, `gate`, or a custom type
registered in `types.custom`. At `bd mol pour` and `bd cook --persist`, a
type that is neither built in nor registered falls back to `task` with a
warning.

`type = "gate"` has two forms:

- **With a `[steps.gate]` block** (an exit-gate): the step becomes a `task`
  bead holding the preparation work, plus a companion `gate` bead that the
  task blocks. Other steps that reference the step id wait for both, so they
  run only after the work is done and the gate is resolved.
- **Without a `[steps.gate]` block**: the step becomes a single `gate` bead
  with no await condition. It is hidden from `bd ready` and is resolved by
  hand.

Async waits and sign-offs are expressed with a `[steps.gate]` block — see
[Gates](#gates) and [Gates in formulas](/workflows/gates#gates-in-formulas).

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

### Conditional steps

A step with a `condition` is left out of a pour when the condition evaluates
false against the pour's variables, and its children go with it:

```toml
[vars.deploy]
default = "false"

[[steps]]
id = "test"
title = "Run tests"

[[steps]]
id = "deploy"
title = "Deploy to production"
needs = ["test"]
condition = "{{deploy}} == true"
```

Nothing names `deploy`, so leaving it out changes nothing else. A step that
stays in the pour should not name a step the pour leaves out in its `needs`,
`depends_on`, or `waits_for` spawner: in releases after v1.3.2 the pour fails
with a formula validation error naming both steps and the condition (v1.3.2
and earlier pour the step with that dependency silently dropped). Give the
dependent step the same condition, or depend only on steps that are always
poured.

## Gates

Add gates for async coordination. A `[steps.gate]` on an ordinary step is an
entry-gate: the step waits for the gate before it can start.

```toml
[[steps]]
id = "deploy"
title = "Deploy to production"

[steps.gate]
type = "timer"
timeout = "1h"    # deploy waits until bd gate check closes the timer, 1h after pour
```

On a step declared `type = "gate"` it is an exit-gate: the step is the
preparation work for the gate's decision, and its task blocks the gate.
Steps that reference it wait for both that work and the gate.

```toml
[[steps]]
id = "approval"
title = "Prepare the release for manager approval"
type = "gate"

[steps.gate]
type = "human"

[[steps]]
id = "deploy"
title = "Deploy to production"
needs = ["approval"]   # waits for the prep task and for bd gate resolve
```

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

## Formula Locations

Formulas are searched in order:
1. `.beads/formulas/` (project-level)
2. `~/.beads/formulas/` (user-level)

`bd formula list` shows everything visible on the search paths.

## Using Formulas

```bash
# List available formulas
bd formula list

# Cook the formula into a proto, then pour it into a molecule
bd cook <formula-file>
bd mol pour <proto-id> --var key=value

# Preview what would be created
bd mol pour <proto-id> --dry-run
```

## Creating Custom Formulas

1. Create file: `.beads/formulas/my-workflow.formula.toml`
2. Define structure (see examples above)
3. Use with: `bd cook my-workflow` then `bd mol pour <proto-id>`

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
