# Graph Links in Beads

Beads supports several types of links between issues to create a knowledge graph. These links enable rich querying and traversal beyond simple blocking dependencies.

## Link Types

### replies_to - Conversation Threading

Creates message threads, similar to email or chat conversations.

**Created by:**
- Orchestrator mail reply commands (orchestrator handles messaging)
- `bd dep add <new-id> <original-id> --type replies_to` (manual linking)

**Use cases:**
- Agent-to-agent message threads
- Discussion chains on issues
- Follow-up communications

**Example:**

```bash
# Original message (via orchestrator mail)
# orchestrator mail send worker/ -s "Review needed" -m "Please review issue-xyz"
# Creates: msg-a1b2

# Reply (automatically sets replies_to)
# orchestrator mail reply msg-a1b2 -m "Done! Approved with minor comments."
# Creates: msg-c3d4 with replies_to: msg-a1b2
```

**Viewing threads:**

```bash
bd show gt-a1b2 --thread
```

### relates_to - Loose Associations

Bidirectional "see also" links between related issues. Not blocking, not hierarchical - just related.

**Created by:**
- `bd relate <id1> <id2>` - Links both issues to each other

**Removed by:**
- `bd unrelate <id1> <id2>` - Removes link in both directions

**Use cases:**
- Cross-referencing related features
- Linking bugs to associated tasks
- Building knowledge graphs
- "See also" connections

**Example:**

```bash
# Link two related issues
bd relate bd-auth bd-security
# Result: bd-auth.relates_to includes bd-security
#         bd-security.relates_to includes bd-auth

# View related issues
bd show bd-auth
# Shows: Related: bd-security

# Remove the link
bd unrelate bd-auth bd-security
```

**Multiple links:**
An issue can have multiple relates_to links:

```bash
bd relate bd-api bd-auth
bd relate bd-api bd-docs
bd relate bd-api bd-tests
# bd-api now relates to 3 issues
```

### duplicates - Deduplication

Marks an issue as a duplicate of a canonical issue. The duplicate is automatically closed.

**Created by:**
- `bd duplicate <id> --of <canonical>`

**Use cases:**
- Consolidating duplicate bug reports
- Merging similar feature requests
- Database deduplication at scale

**Example:**

```bash
# Two similar bug reports exist
bd show bd-bug1  # "Login fails on Safari"
bd show bd-bug2  # "Safari login broken"

# Mark bug2 as duplicate of bug1
bd duplicate bd-bug2 --of bd-bug1
# Result: bd-bug2 is closed with duplicate_of: bd-bug1

# View shows the relationship
bd show bd-bug2
# Status: closed
# Duplicate of: bd-bug1
```

**Behavior:**
- Duplicate issue is automatically closed
- Original (canonical) issue remains open
- `duplicate_of` field stores the canonical ID

### supersedes - Version Chains

Marks an issue as superseded by a newer version. The old issue is automatically closed.

**Created by:**
- `bd supersede <old-id> --with <new-id>`

**Use cases:**
- Design document versions
- Spec evolution
- Artifact versioning
- RFC chains

**Example:**

```bash
# Original design doc
bd create --title "Design Doc v1" --type task
# Creates: bd-doc1

# Later, create updated version
bd create --title "Design Doc v2" --type task
# Creates: bd-doc2

# Mark v1 as superseded
bd supersede bd-doc1 --with bd-doc2
# Result: bd-doc1 closed with superseded_by: bd-doc2

# View shows the chain
bd show bd-doc1
# Status: closed
# Superseded by: bd-doc2
```

**Behavior:**
- Old issue is automatically closed
- New issue remains in its current state
- `superseded_by` field stores the replacement ID

## Schema Fields

These fields are added to issues:

| Field | Type | Description |
|-------|------|-------------|
| `replies_to` | string | ID of parent message (threading) |
| `relates_to` | []string | IDs of related issues (bidirectional) |
| `duplicate_of` | string | ID of canonical issue |
| `superseded_by` | string | ID of replacement issue |

## Querying Links

### View Issue Details

```bash
bd show <id>
```

Shows all link types for an issue:

```
bd-auth: Implement authentication
Status: open
Priority: P1

Related to (3):
  bd-security: Security audit
  bd-users: User management
  bd-sessions: Session handling
```

### View Threads

```bash
bd show <id> --thread
```

Follows `replies_to` chain to show conversation history.

### JSON Output

```bash
bd show <id> --json
```

Returns all fields including graph links:

```json
{
  "id": "bd-auth",
  "title": "Implement authentication",
  "relates_to": ["bd-security", "bd-users", "bd-sessions"],
  "duplicate_of": "",
  "superseded_by": ""
}
```

## Comparison with Dependencies

| Link Type | Blocking? | Hierarchical? | Direction |
|-----------|-----------|---------------|-----------|
| `blocks` | Yes | No | One-way |
| `parent_id` | No | Yes | One-way |
| `relates_to` | No | No | Bidirectional |
| `replies_to` | No | No | One-way |
| `duplicate_of` | No | No | One-way |
| `superseded_by` | No | No | One-way |

## Use Cases

### Knowledge Base

Link related documentation:

```bash
bd relate bd-api-ref bd-quickstart
bd relate bd-api-ref bd-examples
bd relate bd-quickstart bd-install
```

### Bug Triage

Consolidate duplicate reports:

```bash
# Find potential duplicates
bd duplicates

# Merge duplicates
bd duplicate bd-bug42 --of bd-bug17
bd duplicate bd-bug58 --of bd-bug17
```

### Version History

Track document evolution:

```bash
bd supersede bd-rfc1 --with bd-rfc2
bd supersede bd-rfc2 --with bd-rfc3
# bd-rfc3 is now the current version
```

### Message Threading

Build conversation chains (via orchestrator mail):

```bash
# orchestrator mail send dev/ -s "Question" -m "How does X work?"
# orchestrator mail reply msg-q1 -m "X works by..."
# orchestrator mail reply msg-q1.reply -m "Thanks!"
```

## Best Practices

1. **Use relates_to sparingly** - Too many links become noise
2. **Prefer specific link types** - `duplicates` is clearer than generic relates_to
3. **Keep threads shallow** - Deep reply chains are hard to follow
4. **Document supersedes chains** - Note why version changed
5. **Query before creating duplicates** - `bd search` first

## See Also

- [Messaging](messaging.md) - Mail commands and threading
- [Dependencies](QUICKSTART.md#dependencies) - Blocking dependencies
- [CLI Reference](CLI_REFERENCE.md) - All commands
