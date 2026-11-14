# UI/UX Philosophy

Beads CLI follows Tufte-inspired design principles for terminal output, using semantic color tokens with adaptive light/dark mode support via Lipgloss.

## Core Principles

### 1. Maximize Data-Ink Ratio (Tufte)
Only color what demands attention. Every colored element should serve a purpose:
- Navigation landmarks (section headers, group titles)
- Scan targets (command names, flag names)
- Semantic states (success, warning, error, blocked)

**Anti-pattern**: Coloring everything defeats the purpose and creates cognitive overload.

### 2. Semantic Color Tokens
Use meaning-based tokens, not raw colors:

| Token | Semantic Meaning | Use Cases |
|-------|-----------------|-----------|
| `Pass` | Success, completion, ready | Checkmarks, completed items, healthy status |
| `Warn` | Attention needed, caution | Warnings, in-progress items, action required |
| `Fail` | Error, blocked, critical | Errors, blocked items, failures |
| `Accent` | Navigation, emphasis | Headers, links, key information |
| `Muted` | De-emphasized, secondary | Defaults, closed items, metadata |
| `Command` | Interactive elements | Command names, flags |

### 3. Perceptual Optimization (Light/Dark Modes)
Lipgloss `AdaptiveColor` ensures optimal contrast in both terminal modes:

```go
ColorPass = lipgloss.AdaptiveColor{
    Light: "#86b300", // Darker green for light backgrounds
    Dark:  "#c2d94c", // Brighter green for dark backgrounds
}
```

**Why this matters**:
- Light terminals need darker colors for contrast
- Dark terminals need brighter colors for visibility
- Same semantic meaning, optimized perception

### 4. Respect Cognitive Load
Let whitespace and position do most of the work:
- Group related information visually
- Use indentation for hierarchy
- Reserve color for exceptional states

## Color Usage Guide

### When to Color

| Situation | Style | Rationale |
|-----------|-------|-----------|
| Navigation landmarks | Accent | Helps users orient in output |
| Command/flag names | Bold | Creates vertical scan targets |
| Success indicators | Pass (green) | Immediate positive feedback |
| Warnings | Warn (yellow) | Draws attention without alarm |
| Errors | Fail (red) | Demands immediate attention |
| Closed/done items | Muted | Visually recedes, "done" |
| High priority (P0/P1) | Semantic color | Only urgent items deserve color |
| Normal priority (P2+) | Plain | Most items don't need highlighting |

### When NOT to Color

- **Descriptions and prose**: Let content speak for itself
- **Examples in help text**: Keep copy-paste friendly
- **Every list item**: Only color exceptional states
- **Decorative purposes**: Color is functional, not aesthetic

## Ayu Theme

All colors use the [Ayu theme](https://github.com/ayu-theme/ayu-colors) for consistency:

```go
// Semantic colors with light/dark adaptation
ColorPass   = AdaptiveColor{Light: "#86b300", Dark: "#c2d94c"}  // Green
ColorWarn   = AdaptiveColor{Light: "#f2ae49", Dark: "#ffb454"}  // Yellow
ColorFail   = AdaptiveColor{Light: "#f07171", Dark: "#f07178"}  // Red
ColorAccent = AdaptiveColor{Light: "#399ee6", Dark: "#59c2ff"}  // Blue
ColorMuted  = AdaptiveColor{Light: "#828c99", Dark: "#6c7680"}  // Gray
```

## Implementation

All styling is centralized in `internal/ui/styles.go`:

```go
// Render functions for semantic styling
ui.RenderPass("✓")     // Success indicator
ui.RenderWarn("⚠")     // Warning indicator
ui.RenderFail("✗")     // Error indicator
ui.RenderAccent("→")   // Accent/link
ui.RenderMuted("...")  // Secondary info
ui.RenderBold("name")  // Emphasis
ui.RenderCommand("bd") // Command reference
```

## Help Text Styling

Following Tufte's principle of layered information:

1. **Section headers** (`Flags:`, `Examples:`) - Accent color for navigation
2. **Flag names** (`--file`) - Bold for scannability
3. **Type annotations** (`string`) - Muted, reference info
4. **Default values** (`(default: ...)`) - Muted, secondary
5. **Descriptions** - Plain, primary content
6. **Examples** - Plain, copy-paste friendly

## Visual Design System

When adding CLI output features, follow these design principles for consistent,
cognitively friendly visuals.

### No Emoji-Style Icons

Do not use large colored emoji icons like red/orange/yellow/blue/white circles
for priorities or status. They cause cognitive overload and break visual
consistency.

Use small Unicode symbols with semantic colors applied via lipgloss:

- Status: `○ ◐ ● ✓ ❄`
- Priority: `P0`–`P4` label with color (no status glyph)

### Status Icons

Use these symbols consistently across all commands:

```text
○ open        - Available to work (white/default)
◐ in_progress - Currently being worked (yellow)
● blocked     - Waiting on dependencies (red)
✓ closed      - Completed (muted gray)
❄ deferred    - Scheduled for later (blue/muted)
```

### Priority Labels and Colors

Format priority as the P-label with color only (no status glyph — `●` is blocked status):

- `P0`: Red + bold (critical)
- `P1`: Orange (high)
- `P2`: Amber (elevated)
- `P3`–`P4`: Default text

### Issue Type Colors

- `bug`: Red (problems need attention)
- `epic`: Purple (larger scope)
- Others: Default text

### Design Principles

1. Small Unicode symbols only; avoid emoji blobs.
2. Semantic colors only for actionable items; do not color everything.
3. Closed items fade using muted gray.
4. Prefer icons over text labels for scanability.
5. Keep icons consistent across list, graph, show, and related commands.
6. Use tree connectors (`├──`, `└──`, `│`) for hierarchies.
7. Reduce cognitive noise; do not show `needs:1` when it is just the parent epic.

### Semantic Styles

Use exported styles from `internal/ui/styles.go`:

```go
// Status styles
ui.StatusInProgressStyle  // Yellow - active work
ui.StatusBlockedStyle     // Red - needs attention
ui.StatusClosedStyle      // Muted gray - done

// Priority styles
ui.PriorityP0Style        // Red + bold
ui.PriorityP1Style        // Orange

// Type styles
ui.TypeBugStyle           // Red
ui.TypeEpicStyle          // Purple

// General styles
ui.PassStyle, ui.WarnStyle, ui.FailStyle
ui.MutedStyle, ui.AccentStyle
ui.RenderMuted(text), ui.RenderAccent(text)
```

Example:

```go
switch issue.Status {
case types.StatusOpen:
    icon = "○"
case types.StatusInProgress:
    icon = ui.StatusInProgressStyle.Render("◐")
case types.StatusBlocked:
    icon = ui.StatusBlockedStyle.Render("●")
case types.StatusClosed:
    icon = ui.StatusClosedStyle.Render("✓")
}
```

## CLI Design Principles

**Minimize cognitive overload.** Every new command, flag, or option adds cognitive burden for users. Before adding anything:

1. **Recovery/fix operations → `bd doctor --fix`**: Don't create separate commands like `bd recover` or `bd repair`. Doctor already detects problems - let `--fix` handle remediation. This keeps all health-related operations in one discoverable place.
   For git hook marker migration specifically: use `bd migrate hooks --dry-run` to preview operations, and `bd doctor --fix` for the standard apply path.

2. **Prefer flags on existing commands**: Before creating a new command, ask: "Can this be a flag on an existing command?" Example: `bd list --stale` instead of `bd stale`.

3. **Consolidate related operations**: Related operations should live together. Version control uses `bd vc {log,diff,commit}`, not separate top-level commands.

4. **Count the commands**: Run `bd --help` and count. If we're approaching 30+ commands, we have a discoverability problem. Consider subcommand grouping.

5. **New commands need strong justification**: A new command should represent a fundamentally different operation, not just a convenience wrapper.

## References

- Tufte, E. (2001). *The Visual Display of Quantitative Information*
- [Ayu Theme Colors](https://github.com/ayu-theme/ayu-colors)
- [Lipgloss - Terminal Styling](https://github.com/charmbracelet/lipgloss)
- [WCAG Color Contrast Guidelines](https://www.w3.org/WAI/WCAG21/Understanding/contrast-minimum.html)
