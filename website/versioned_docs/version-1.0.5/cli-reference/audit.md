---
id: audit
title: bd audit
slug: /cli-reference/audit
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc audit`

## bd audit

Audit log entries are appended to .beads/interactions.jsonl.

Each line is one event. This file is intended to be versioned in git and used for:
- auditing ("why did the agent do that?")
- dataset generation (SFT/RL fine-tuning)

Entries are append-only. Labeling creates a new "label" entry that references a parent entry.

```
bd audit
```

### bd audit label

Append a label entry referencing an existing interaction

```
bd audit label <entry-id> [flags]
```

**Flags:**

```
      --label string    Label value (e.g. "good" or "bad")
      --reason string   Reason for label
```

### bd audit record

Append an audit interaction entry

```
bd audit record [flags]
```

**Flags:**

```
      --error string       Error string (llm_call/tool_call)
      --exit-code int      Exit code (tool_call) (default -1)
      --issue-id string    Related issue id (bd-...)
      --kind string        Entry kind (e.g. llm_call, tool_call, label)
      --model string       Model name (llm_call)
      --prompt string      Prompt text (llm_call)
      --response string    Response text (llm_call)
      --stdin              Read a JSON object from stdin (must match audit.Entry schema)
      --tool-name string   Tool name (tool_call)
```
