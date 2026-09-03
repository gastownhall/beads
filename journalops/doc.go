// Package journalops describes the workspace's DURABLE MUTATION JOURNAL: the
// seq-ordered record of every committed bead mutation that `bd events tail`,
// `bd events export` and GET /v0/beads/events read, and that an external
// consumer replays to rebuild its own copy of the graph.
//
// It is a sibling of issueops and memoryops rather than more methods on either,
// and the reason is not that the role rule forbids appending (it does): a
// journal record is not a bead and not a setting. It is ENGINE STATE — rows in
// the clone-local, dolt_ignored bd_events_journal table, written inside the
// same transaction as the mutation they describe (migration 0064) — so it
// survives no merge, travels through no pull, and describes the history of ONE
// clone rather than the state of a project. A plane whose rows are deliberately
// unversioned has nothing in common with a plane whose rows are the versioned
// data, however similar an id column makes them look.
//
// WHAT A CONSUMER IS ACTUALLY HOLDING. The journal is a REPLAY FEED, not a
// query surface. Every promise here — seq order, the exclusive checkpoint, the
// head that travels with its rows, the typed truncation — exists so that a
// consumer polling from a stored checkpoint can prove it has missed nothing.
// That is why a checkpoint below the retained window is an ERROR rather than a
// smaller answer: at the SQL level "nothing new" and "your prefix was deleted"
// are one empty result set, and a reader that guessed would either stall
// forever or skip to the current floor and lose every record in between. Both
// are silent data loss, which is the one failure a replay feed must never ship.
//
// THE VOCABULARY OF Row.Op IS NOT DECLARED HERE, and that is deliberate. The
// engine journals seven ops and the public event vocabulary is six — the
// seventh, a comment write, is journaled but mints no wire event, because a
// comment's effect is already visible in the next issue snapshot. That
// reconciliation belongs to the emitter and to the wire contract
// (internal/storage/issueops: WireEventOps, EngineOnlyEventOps, IsWireEventOp),
// which are the two places that have to agree about it. Naming a closed set
// here would put a third copy one op away from drifting, and would tell a
// caller that reads records that it is entitled to an opinion about which ones
// a projector forwards. Op is a string, and what the strings mean is stated
// where they are minted.
//
// THERE IS NO PRUNE ON THIS ROLE, and no activation switch either. Both exist —
// `bd events prune`, the automatic bounding, and the per-instance enable that
// binds journaling to one open workspace — and both are OPERATOR surface,
// carried by storage.EventsJournalAccessor and storage.EventsJournalConfigurer
// beside the rows. The split is about who holds what: a surface that only
// PUBLISHES the journal, which is what `bd serve` documents itself as, must not
// be one line away from a delete. Narrowing the interface is what makes "this
// server cannot prune" a fact about the type rather than a promise about the
// handler. A role that carried a retention decision it is documented never to
// take would give that fact away for nothing.
//
// AND THERE IS NO ACCESSOR. Every other role in this tree is handed out by a
// method on a store or a unit-of-work provider; this one is reached by TYPE
// ASSERTION, because the journal is not part of storage.DoltStorage's published
// surface and a backend is free not to implement it at all. issueops.Importer is
// the precedent — a role with one accessor and none on the store interface —
// and the consequence is the same: the conformance census finds this role by
// parsing source rather than by reflecting over accessors
// (backend/conformance/role_coverage_scan_test.go), which is what keeps it from
// being invisible to the exhaustiveness gate.
//
// WHAT THIS PACKAGE IMPORTS: context and fmt, and nothing else — narrower even
// than memoryops, which needs beadserrors for a sentinel. This plane has no
// deterministic request-validation refusal to classify: a checkpoint is an
// int64, a limit is an int, and the one refusal that exists is the truncation,
// which is a TYPED error carrying a window rather than a sentinel a caller
// matches by identity. If a validation refusal ever appears here it aliases
// beadserrors.ErrValidation rather than minting a twin, for the reasons
// memoryops/errors.go gives.
package journalops
