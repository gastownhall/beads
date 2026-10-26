---
title: "bd versions"
description: "List the recorded versions of a bead"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc versions`.

List the versions recorded for a bead by versioned history.

Recording is off by default. Turn it on with:

  bd config set versioned-history.enabled true

That writes the setting into the store, so every client of that store agrees
about whether a write is recorded, and it is the setting this command reports.
BD_VERSIONED_HISTORY_ENABLED=1 (or a value in config.yaml) also turns recording
on for the writes a process makes, but it says nothing about the store, and
this command only reads, so it does not consult them.

Either source turning it on is enough for a writer; neither can switch the
other off. The consequence worth knowing: once the store setting says on,
BD_VERSIONED_HISTORY_ENABLED=0 will not turn recording off for one run. Turn
it off where it was turned on:

  bd config set versioned-history.enabled false

Only beads created while recording is on, or that enter the issues plane
while it is on (a wisp promoted or made persistent), are versioned. A bead
already on the issues plane when recording was turned on is legacy: editing
it records nothing (the write fence, design §16.2b), and nothing is
backfilled.

Turning recording off does not hide what was already recorded — this command
still lists it, and says that recording is currently off.

A caution while the feature is young: keep recording ON for every writer of a
shared store, or leave it off for all of them. A write from a client with
recording off advances nothing, and the next recorded write mints a version
that absorbs that unrecorded change under its own actor — the revisions stay
contiguous and the listing looks gapless when it is not.

Examples:
  bd versions bd-123
  bd versions bd-123 --json

```
bd versions <id> [flags]
```
