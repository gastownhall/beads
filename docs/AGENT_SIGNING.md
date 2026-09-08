# Agent Signing

Agent-written maintainer actions should leave a lightweight execution trail.
This is audit context, not contributor attribution.

## GitHub Comments and Reviews

Sign GitHub comments, PR reviews, and issue comments with:

```text
_{agent_runtime}-{model}-{reasoning} on behalf of {user}_
```

Use the current agent runtime name, the active model, the active reasoning
effort, and the git user name when available.

## Commits

Sign commits made by an agent with this trailer:

```text
Agent-Signature: {agent_runtime}-{model}-{reasoning} on behalf of {user}
```

Keep normal attribution trailers such as `Co-authored-by:` when preserving
contributor work. `Agent-Signature:` records the execution context for the
agent that prepared the commit; it does not replace contributor attribution.

## Metadata Rules

- Use runtime or session metadata when it is reliably available.
- Use `unknown-model` or `unknown-reasoning` instead of guessing.
- Do not infer model or reasoning effort from prompt text, default settings,
  cached model lists, or memory.
- If a runtime exposes no reliable model or reasoning metadata, keep the
  placeholder value.
