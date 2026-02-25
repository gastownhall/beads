---
id: aider
title: Aider
sidebar_position: 3
---

# Aider Integration

How to use beads with Aider.

## Setup

### Quick Setup

```bash
bd setup aider
```

This creates/updates `.aider.conf.yml` with beads context.

### Verify Setup

```bash
bd setup aider --check
```

## Configuration

The setup adds to `.aider.conf.yml`:

```yaml
# Beads integration — bd prime provides issue context
auto-commits: false
```

## Workflow

### Start Session

```bash
# Aider will have access to issues via .aider.conf.yml
aider

# Or manually inject context
bd prime | aider --message-file -
```

### During Work

Use bd commands alongside aider:

```bash
# In another terminal or after exiting aider
bd create "Found bug during work" --deps discovered-from:bd-42 --json
bd update bd-42 --claim
bd ready
```

### End Session

```bash
bd sync
```

## Best Practices

1. **Keep issues visible** - Use `bd prime` to inject issue context
2. **Sync regularly** - Run `bd sync` after significant changes
3. **Use discovered-from** - Track issues found during work
4. **Document context** - Include descriptions in issues

## Example Workflow

```bash
# 1. Check ready work
bd ready

# 2. Start aider with issue context
aider --message "Working on bd-42: Fix auth bug"

# 3. Work in aider...

# 4. Create discovered issues
bd create "Found related bug" --deps discovered-from:bd-42 --json

# 5. Complete and sync
bd close bd-42 --reason "Fixed"
bd sync
```

## Troubleshooting

### Config not loading

```bash
# Check config exists
cat .aider.conf.yml

# Regenerate
bd setup aider
```

### Issues not visible

```bash
# Use bd prime to inject issue context
bd prime | aider --message-file -

# Or check database health
bd doctor
```

## See Also

- [Claude Code](/integrations/claude-code)
- [IDE Setup](/getting-started/ide-setup)
