# Agent Beads

> Adapted from ACF beads skill

**v0.40+**: First-class support for agent tracking via `type=agent` beads.

## When to Use Agent Beads

| Scenario | Agent Bead? | Why |
|----------|-------------|-----|
| Multi-agent orchestration | Yes | Track state, assign work via slots |
| Single Claude session | No | Overkill—just use regular beads |
| Long-running background agents | Yes | Heartbeats enable liveness detection |
| Role-based agent systems | Yes | Role beads define agent capabilities |

## Bead Types

| Type | Purpose | Has Slots? |
|------|---------|------------|
| `agent` | AI agent tracking | Yes (hook, role) |
| `role` | Role definitions for agents | No |

Other types (`task`, `bug`, `feature`, `epic`) remain unchanged.

## State Machine

Agent beads track state for coordination:

```
idle → spawning → running/working → done → idle
                       ↓
                    stuck → (needs intervention)
```

**Key states**: `idle`, `spawning`, `running`, `working`, `stuck`, `done`, `stopped`, `dead`

The `dead` state is set by Witness (monitoring system) via heartbeat timeout—agents don't set this themselves.

## Slot Architecture

Slots are named references from agent beads to other beads:

| Slot | Cardinality | Purpose |
|------|-------------|---------|
| `hook` | 0..1 | Current work attached to agent |
| `role` | 1 | Role definition bead (required) |

**Why slots?** They enforce constraints (one work item at a time) and enable queries like "what is agent X working on?" or "which agent has this work?"

## Monitoring Integration

Agent beads enable:

- **Witness System**: Monitors agent health via heartbeats
- **State Coordination**: ZFC-compliant state machine for multi-agent systems
- **Work Attribution**: Track which agent owns which work

## CLI Reference

Run `bd agent --help` for state/heartbeat/show commands.
Run `bd slot --help` for set/clear/show commands.
Run `bd create --help` for `--type=agent` and `--type=role` options.
