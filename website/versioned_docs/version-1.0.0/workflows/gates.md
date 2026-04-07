---
id: gates
title: Gates
sidebar_position: 4
---

# Gates

Gates are async coordination primitives for workflow orchestration.

## What are Gates?

Gates block step progression until a condition is met:
- Human approval
- Timer expiration
- External event (GitHub PR, CI, etc.)

## Gate Types

### Human Gate

Wait for human approval:

```toml
[[steps]]
id = "deploy-approval"
title = "Approval for production deploy"
type = "human"

[steps.gate]
type = "human"
approvers = ["team-lead", "security"]
require_all = false  # Any approver can approve
```

### Timer Gate

Wait for a duration:

```toml
[[steps]]
id = "cooldown"
title = "Wait for cooldown period"

[steps.gate]
type = "timer"
duration = "24h"
```

Durations: `30m`, `2h`, `24h`, `7d`

### GitHub Gate

Wait for GitHub events:

```toml
[[steps]]
id = "wait-for-ci"
title = "Wait for CI to pass"

[steps.gate]
type = "github"
event = "check_suite"
status = "success"
```

```toml
[[steps]]
id = "wait-for-merge"
title = "Wait for PR merge"

[steps.gate]
type = "github"
event = "pull_request"
action = "closed"
merged = true
```

## Gate States

| State | Description |
|-------|-------------|
| `pending` | Waiting for condition |
| `open` | Condition met, can proceed |
| `closed` | Step completed |

## Using Gates in Workflows

### Approval Flow

```toml
formula = "production-deploy"

[[steps]]
id = "build"
title = "Build production artifacts"

[[steps]]
id = "staging"
title = "Deploy to staging"
needs = ["build"]

[[steps]]
id = "qa-approval"
title = "QA sign-off"
needs = ["staging"]
type = "human"

[steps.gate]
type = "human"
approvers = ["qa-team"]

[[steps]]
id = "production"
title = "Deploy to production"
needs = ["qa-approval"]
```

### Scheduled Release

```toml
formula = "scheduled-release"

[[steps]]
id = "prepare"
title = "Prepare release"

[[steps]]
id = "wait-window"
title = "Wait for release window"
needs = ["prepare"]

[steps.gate]
type = "timer"
duration = "2h"

[[steps]]
id = "deploy"
title = "Deploy release"
needs = ["wait-window"]
```

### CI Integration

```toml
formula = "ci-gated-deploy"

[[steps]]
id = "create-pr"
title = "Create pull request"

[[steps]]
id = "wait-ci"
title = "Wait for CI"
needs = ["create-pr"]

[steps.gate]
type = "github"
event = "check_suite"
status = "success"

[[steps]]
id = "merge"
title = "Merge PR"
needs = ["wait-ci"]
type = "human"
```

## Gate Operations

### Check Gate Status

```bash
bd show bd-xyz.3  # Shows gate state
bd show bd-xyz.3 --json | jq '.gate'
```

### Manual Gate Override

For human gates:

```bash
bd gate approve bd-xyz.3 --approver "team-lead"
```

### Skip Gate (Emergency)

```bash
bd gate skip bd-xyz.3 --reason "Emergency deploy"
```

## waits-for Dependency

The `waits-for` dependency type creates fan-in patterns:

```toml
[[steps]]
id = "test-a"
title = "Test suite A"

[[steps]]
id = "test-b"
title = "Test suite B"

[[steps]]
id = "integration"
title = "Integration tests"
waits_for = ["test-a", "test-b"]  # Fan-in: waits for all
```

## Best Practices

1. **Use human gates for critical decisions** - Don't auto-approve production
2. **Add timeout to timer gates** - Prevent indefinite blocking
3. **Document gate requirements** - Make approvers clear
4. **Use CI gates for quality** - Block on test failures
