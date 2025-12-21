# UI/UX Philosophy

Beads CLI follows Tufte-inspired design principles for terminal output.

## Core Principles

1. **Maximize data-ink ratio**: Only color what demands attention
2. **Respect cognitive load**: Let whitespace and position do most of the work
3. **Create scannable hierarchy**: Headers mark territory, bold creates scan targets

## Color Usage

| Purpose | Style | When to Use |
|---------|-------|-------------|
| Navigation landmarks | Accent (blue) | Section headers, group titles |
| Scan targets | Bold | Command names, flag names |
| De-emphasized | Muted (gray) | Types, defaults, closed items |
| Semantic states | Pass/Warn/Fail | P0/P1 priority, bugs, blocked |
| Standard text | Plain | Descriptions, prose, examples |

## Anti-Patterns

- Don't highlight everything (defeats the purpose)
- Don't use color for decoration
- Don't style closed/completed items (they're done, users don't care)
- Keep examples plain (copy-paste friendly)

## Help Output Styling

### Main Help (`bd help`)

- **Group headers** (Working With Issues:, Views & Reports:, etc.): Accent color
- **Command names** in listings: Bold
- **Command references** in descriptions ('comments add'): Bold

### Subcommand Help (`bd <cmd> --help`)

- **Section headers** (Usage:, Flags:, Examples:): Accent color
- **Flag names** (-f, --file): Bold
- **Type annotations** (string, int, duration): Muted
- **Default values** (default: ...): Muted
- **Descriptions**: Plain
- **Examples**: Plain (copy-paste friendly)

## Ayu Theme

All colors use the Ayu theme with adaptive light/dark mode support.
See `internal/ui/styles.go` for implementation.

| Color | Light Mode | Dark Mode | Usage |
|-------|------------|-----------|-------|
| Accent | #399ee6 | #59c2ff | Headers, links |
| Command | Bold white | Bold white | Command/flag names |
| Muted | #828c99 | #6c7680 | Types, defaults |
| Pass | #6cbf43 | #7fd962 | Success states |
| Warn | #e6ba7e | #ffb454 | Warnings, P1 |
| Fail | #f07171 | #f26d78 | Errors, bugs, P0 |
