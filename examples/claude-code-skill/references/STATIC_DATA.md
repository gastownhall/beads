# Using bd for Static Reference Data

bd is primarily designed for work tracking, but can also serve as a queryable database for static reference data with some adaptations.

## Work Tracking (Primary Use Case)

Standard bd workflow:
- Issues flow through states (open → in_progress → closed)
- Priorities and dependencies matter
- Status tracking is essential
- IDs are sufficient for referencing

## Reference Databases / Glossaries (Alternative Use)

When using bd for static data (terminology, glossaries, reference information):

**Characteristics:**
- Entities are mostly static (typically always open)
- No real workflow or state transitions
- Names/titles more important than IDs
- Minimal or no dependencies

**Recommended approach:**
- Use separate database (not mixed with work tracking) to avoid confusion
- Consider dual format: maintain markdown version alongside database for name-based lookup
- Example: A terminology database could use both `terms.db` (queryable via bd) and `GLOSSARY.md` (browsable by name)

**Key difference**: Work items have lifecycle; reference entities are stable knowledge.

## When to Use This Pattern

**Good fit:**
- Technical glossaries or terminology databases
- Reference documentation that needs dependency tracking
- Knowledge bases with relationships between entries
- Structured data that benefits from queryability

**Poor fit:**
- Data that changes frequently (use work tracking pattern)
- Simple lists (markdown is simpler)
- Data that needs complex queries (use proper database)

## Limitations

**bd show requires IDs, not names:**
- `bd show term-42` works
- `bd show "API endpoint"` doesn't work
- Workaround: `bd list | grep -i "api endpoint"` to find ID first
- This is why dual format (bd + markdown) is recommended for reference data

**No search by content:**
- bd searches by ID, title filters, status, labels
- For full-text search across descriptions/notes, use grep on the JSONL file
- Example: `grep -i "authentication" .beads/issues.jsonl`
