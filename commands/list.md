---
description: List issues with optional filters
argument-hint: [--status] [--priority] [--type] [--assignee] [--label]
---

List beads issues with optional filtering.

## Basic Filters

- **--status, -s**: Filter by status (open, in_progress, blocked, closed)
- **--priority, -p**: Filter by priority (0-4: 0=critical, 1=high, 2=medium, 3=low, 4=backlog)
- **--type, -t**: Filter by type (bug, feature, task, epic, chore)
- **--assignee, -a**: Filter by assignee
- **--label, -l**: Filter by labels (comma-separated, must have ALL labels)
- **--label-any**: Filter by labels (OR semantics, must have AT LEAST ONE)
- **--title**: Filter by title text (case-insensitive substring match)
- **--limit, -n**: Limit number of results

## Advanced Filters

### Pattern Matching
- **--title-contains**: Search for text in title (case-insensitive)
- **--desc-contains**: Search for text in description (case-insensitive)
- **--notes-contains**: Search for text in notes (case-insensitive)

### Date Ranges
- **--created-after**: Issues created after date (YYYY-MM-DD or ISO 8601)
- **--created-before**: Issues created before date
- **--updated-after**: Issues updated after date
- **--updated-before**: Issues updated before date
- **--closed-after**: Issues closed after date
- **--closed-before**: Issues closed before date

### Priority Range
- **--priority-min**: Minimum priority (inclusive)
- **--priority-max**: Maximum priority (inclusive)

### Empty/Null Checks
- **--empty-description**: Find issues with no description
- **--no-assignee**: Find unassigned issues
- **--no-labels**: Find issues with no labels

## Examples

### Basic Usage
- `bd list --status open --priority 1`: High priority open issues
- `bd list --type bug --assignee alice`: Alice's assigned bugs
- `bd list --label backend,needs-review`: Backend issues needing review
- `bd list --title "auth"`: Issues with "auth" in the title

### Advanced Usage
- `bd list --title-contains "auth" --status open`: Search open issues for auth-related work
- `bd list --priority-min 0 --priority-max 1`: Critical and high priority issues only
- `bd list --created-after 2025-01-01 --status open`: Recent open issues
- `bd list --empty-description --status open`: Open issues missing descriptions
- `bd list --no-assignee --priority 1`: High priority unassigned work
- `bd list --desc-contains "TODO" --notes-contains "review"`: Find items needing attention

## Output Formats

- Default: Human-readable table
- `--json`: JSON format for scripting
- `--format digraph`: Graph format for golang.org/x/tools/cmd/digraph
- `--format dot`: Graphviz DOT format
