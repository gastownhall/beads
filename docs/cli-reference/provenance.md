---
title: "bd provenance"
description: "Append-only provenance event log"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc provenance`.

Record and read provenance events: typed bindings from an issue to an
opaque external artifact (a git SHA, PR, work-id, transcript, or branch).

The log is append-only — there is no update or delete. bd never interprets the
actor or ref; only kind and ref-kind are structurally validated. Recording is
idempotent on a deterministic id, so a producer firing twice is harmless.

```
bd provenance [command]
```

## bd provenance by-ref

List provenance events bound to a ref

```
bd provenance by-ref <ref> [flags]
```

## bd provenance log

List provenance events for an issue

```
bd provenance log <issue-id> [flags]
```

**Flags:**

```
      --kind string   filter by kind (optional)
```

## bd provenance record

Record a provenance event. The event is appended idempotently: a
deterministic id is computed from source:issue:kind:(ref or --at), so re-running
the same record is a no-op.

An event recorded without --ref requires --at so the id is caller-owned.

```
bd provenance record --issue <id> --kind <k> --source <s> [flags]
```

**Flags:**

```
      --actor string      opaque actor identifier (optional)
      --at string         event-time as RFC3339 (required for ref-less kinds)
      --issue string      issue id (required)
      --kind string       event kind: cut|claim|suspend|resume|handoff|commit|land|used (required)
      --payload string    opaque payload, e.g. JSON (optional)
      --ref string        opaque external reference, e.g. a SHA or PR url (optional)
      --ref-kind string   ref kind: git-sha|pr|work-id|transcript|branch (optional)
      --source string     producer of the event, e.g. git-hook, orchestrator (required)
```
