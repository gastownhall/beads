# Issue Metadata

The `metadata` field on issues accepts arbitrary JSON. Any valid JSON value is stored as-is.

## Reserved Key Prefixes

| Prefix | Reserved For |
|--------|------------|
| `bd:` | Beads internal use |
| `_` | Internal/private keys |

Avoid these prefixes in user-defined keys to prevent conflicts with future Beads features.

## Related

- [#1416](https://github.com/steveyegge/beads/issues/1416) — Optional schema enforcement (future)
