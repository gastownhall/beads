# External GC interruption: open design choices

Status: draft proposal. No interruption policy, timeout, or runtime change is selected.
Refs [#6918](https://github.com/gastownhall/beads/issues/6918).

## Existing boundary

At source snapshot `25a54a73`, `runCompactDolt` in `cmd/bd/compact.go` launches
external `dolt gc` with `exec.Command`, including the plain-GC fallback.
Landed [#6327](https://github.com/gastownhall/beads/pull/6327) (`8269a88c`) checks
context cancellation during directory resolution; its collectors still use `exec.Command`.
Canceling that Go context therefore does not itself cancel a running collector.
This does not make GC uninterruptible: terminal signals, process termination,
and host shutdown are separate mechanisms whose effects need host-specific evidence.

Replacing the calls mechanically with `exec.CommandContext` would select its
default `Process.Kill` cancellation behavior. That is a data-integrity decision,
not just context plumbing. The [adoption review](https://github.com/gastownhall/beads/pull/6327#issuecomment-5877102997)
records successful live-server GC for the tested binary; it does not establish
safe interruption across Dolt versions, hosts, or collection phases.

## Decisions before and after launch

Before launch, decide which cancellation checkpoints apply to directory resolution,
the primary command, and the fallback. Refusal before either command starts can
report that no collector ran. Cancellation between attempts cannot make that claim:
the primary command has already run, even if its output permits a fallback.

After launch, the alternatives remain open:

| Alternative | Evidence or tradeoff to resolve |
|---|---|
| Wait for the collector to exit | Cancellation may not shorten the wait; define reporting and who owns the child if the caller exits. |
| Request a supported graceful interruption | Establish the Dolt mechanism and host delivery semantics; decide separately whether escalation is allowed. |
| Terminate the collector forcibly | Establish data integrity, recovery, and process cleanup at each supported interruption point before adopting it. |

None of these alternatives implies a timeout, escalation interval, signal choice,
or permission to terminate the SQL server. Any engine recovery mechanism belongs
at the storage/driver boundary described in the [project charter](../PROJECT_CHARTER.md#storage-boundary),
rather than a new Beads-side recovery algorithm.

## Evidence needed for a runtime proposal

Use disposable copies with known tables, rows, refs, and retained history, plus an
uninterrupted control for the same Dolt executable, arguments, and server mode.
Record the selected and launched executable/version, host, directory, live-server
identity, interruption request, observed delivery, exit, and surviving processes.
Exercise plain GC and the archive-level path where supported; do not infer CLI
capability from the SQL or server-configuration surface.

Choose interruption points from the engine's actual phases: before launch, during
collection, around publication of rewritten storage, during cleanup, and between
primary and fallback attempts. Timing alone is not proof of the phase reached.
For each supported offline or live-server mode, check retained data and reads,
subsequent writes, reopen/restart behavior, documented recovery, and another GC.
Keep unrelated database and server sentinels intact. Record unsupported combinations
and failures instead of generalizing one successful host/version probe.

A useful falsifier is an interrupted run that exits promptly but loses retained
data, cannot accept a later write, leaves an unowned child, or needs undocumented
repair. It defeats that proposed behavior even if the command reports cancellation.

## Reporting and compatibility

The eventual contract must distinguish a cancellation request, collector completion,
collector failure, and interrupted collection with an unknown recovery outcome.
An exit code or canceled context alone does not prove collection completed safely.
Decide text/JSON and exit behavior together, including a completed first attempt
whose fallback is refused; this note selects none of those public responses.

Directory authority, existing mode/read-only gates, fallback classification, and
advisory sizing remain unchanged. PR #6327 has landed while
[#5668](https://github.com/gastownhall/beads/pull/5668) remains open. Preserve the
paired changes' contributor attribution and review partial reverts together.
Maintainer agreement on the supported choices and evidence precedes a runtime fix.
