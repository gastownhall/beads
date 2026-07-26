//! Curated CLI scenarios, each exercising a specific `bd` contract behavior.
//! Run against the reference `bd` they become the golden ground truth; run
//! against the candidate `bd` they are the differential parity test.
//!
//! `all()` are the curated, maintained scenarios that always run. `catalog()`
//! loads the much larger enumerated set (`scenarios/enumerated.json`, ~500
//! deterministic scenarios covering the wider bd CLI surface as data) — the
//! opt-in deep tier, pulled in only when `ORACLE_CATALOG` is set.

use crate::differential::Scenario;
use serde::Deserialize;
use std::path::PathBuf;

#[derive(Deserialize)]
struct CatalogEntry {
    name: String,
    prefix: String,
    #[serde(default)]
    steps: Vec<Vec<String>>,
    #[serde(default = "yes")]
    deterministic: bool,
}
fn yes() -> bool {
    true
}

/// Load the enumerated catalog (deterministic, byte-diffable scenarios only).
/// Non-deterministic ones (ID minting, ready-recency) are excluded — they're
/// asserted via properties elsewhere, not byte-diff.
pub fn catalog() -> Vec<Scenario> {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("scenarios/enumerated.json");
    let data = match std::fs::read_to_string(&path) {
        Ok(d) => d,
        Err(_) => return Vec::new(),
    };
    let entries: Vec<CatalogEntry> = serde_json::from_str(&data).unwrap_or_default();
    let curated: std::collections::HashSet<String> =
        all().into_iter().map(|s| s.name).collect();
    entries
        .into_iter()
        .filter(|e| e.deterministic && !e.steps.is_empty() && !curated.contains(&e.name))
        .map(|e| Scenario {
            name: e.name,
            prefix: e.prefix,
            steps: e.steps,
            ordered: false,
        })
        .collect()
}

/// All scenarios. Flags here encode our current understanding of bd's CLI; a
/// wrong flag simply captures bd's "unknown flag" response in the golden, which
/// is itself the correction.
pub fn all() -> Vec<Scenario> {
    vec![
        // #1 CRITICAL: close-blocker -> dependent becomes ready (the headline close→ready loop).
        Scenario::new(
            "graph_close_ready",
            "g",
            &[
                &["create", "Root issue", "--id", "g-root", "--force", "-p", "1", "-t", "feature", "-d", "root", "--json"],
                &["create", "Dep issue", "--id", "g-dep", "--force", "-p", "2", "-t", "task", "-d", "dep", "--json"],
                &["dep", "add", "g-root", "g-dep", "--type", "blocks", "--json"],
                &["ready", "--json"],
                &["close", "g-dep", "--force", "--json"],
                &["ready", "--json"],
                &["show", "g-root", "--json"],
            ],
        ),
        // #5 HIGH: ordering. Pins priority ASC, created_at DESC, id ASC across 4 issues.
        Scenario::new(
            "ordering",
            "o",
            &[
                &["create", "A", "--id", "o-a", "--force", "-p", "2", "-t", "task", "--json"],
                &["create", "B", "--id", "o-b", "--force", "-p", "1", "-t", "task", "--json"],
                &["create", "C", "--id", "o-c", "--force", "-p", "2", "-t", "task", "--json"],
                &["create", "D", "--id", "o-d", "--force", "-p", "1", "-t", "task", "--json"],
                &["list", "--all", "--json"],
                &["ready", "--json"],
            ],
        ),
        // #5b ORDER PARITY: all-DISTINCT priorities, so `priority ASC` fully determines
        // the sequence independent of timestamp precision. Marked `.ordered()` so the
        // harness asserts the exact order — the dedicated check that closes the multiset
        // blind spot. Created scrambled (P3,P0,P2,P1) → list/ready must emit P0,P1,P2,P3.
        Scenario::new(
            "list_order_by_priority",
            "lop",
            &[
                &["create", "A", "--id", "lop-a", "--force", "-p", "3", "-t", "task", "--json"],
                &["create", "B", "--id", "lop-b", "--force", "-p", "0", "-t", "task", "--json"],
                &["create", "C", "--id", "lop-c", "--force", "-p", "2", "-t", "task", "--json"],
                &["create", "D", "--id", "lop-d", "--force", "-p", "1", "-t", "task", "--json"],
                &["list", "--json"],
                &["ready", "--json"],
            ],
        )
        .ordered(),
        // CRITICAL: cycle detection + self-dep rejection (exact error + exit code).
        Scenario::new(
            "cycle_reject",
            "c",
            &[
                &["create", "A", "--id", "c-a", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "c-b", "--force", "-t", "task", "--json"],
                &["dep", "add", "c-a", "c-b", "--type", "blocks", "--json"],
                &["dep", "add", "c-b", "c-a", "--type", "blocks", "--json"],
                &["dep", "add", "c-a", "c-a", "--type", "blocks", "--json"],
            ],
        ),
        // CRITICAL: dependency retype. bd REJECTS it (exact error + exit code pinned).
        Scenario::new(
            "dep_retype",
            "r",
            &[
                &["create", "A", "--id", "r-a", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "r-b", "--force", "-t", "task", "--json"],
                &["dep", "add", "r-a", "r-b", "--type", "blocks", "--json"],
                &["dep", "add", "r-a", "r-b", "--type", "related", "--json"],
            ],
        ),
        // HIGH: storage tiers — history vs ephemeral vs no-history set algebra.
        Scenario::new(
            "tiers_ephemeral",
            "t",
            &[
                &["create", "Normal", "--id", "t-n", "--force", "-t", "task", "--json"],
                &["create", "Wisp", "--id", "t-w", "--force", "-t", "task", "--ephemeral", "--json"],
                &["create", "NoHist", "--id", "t-h", "--force", "-t", "task", "--no-history", "--json"],
                &["ready", "--json"],
                &["ready", "--include-ephemeral", "--json"],
                &["list", "--all", "--json"],
                &["count", "--json"],
            ],
        ),
        // CRITICAL: claim lifecycle + idempotent self-reclaim.
        Scenario::new(
            "claim_lifecycle",
            "k",
            &[
                &["create", "Work", "--id", "k-w", "--force", "-t", "task", "--json"],
                &["ready", "--json"],
                &["update", "k-w", "--claim", "--json"],
                &["ready", "--json"],
                &["update", "k-w", "--claim", "--json"],
                &["show", "k-w", "--json"],
            ],
        ),
        // HIGH: not-found surfaces on read and on mutation.
        Scenario::new(
            "error_notfound",
            "e",
            &[
                &["show", "e-nope", "--json"],
                &["update", "e-nope", "--status", "closed", "--json"],
            ],
        ),
        // HIGH: metadata storage + field filtering (the index-richness question).
        Scenario::new(
            "metadata_filter",
            "m",
            &[
                &["create", "Hot", "--id", "m-1", "--force", "-t", "task", "--metadata", r#"{"team":"core","heat":"hot"}"#, "--json"],
                &["create", "Cold", "--id", "m-2", "--force", "-t", "task", "--metadata", r#"{"team":"core","heat":"cold"}"#, "--json"],
                &["show", "m-1", "--json"],
                &["list", "--all", "--metadata-field", "heat=hot", "--json"],
                &["list", "--all", "--metadata-field", "team=core", "--json"],
                &["list", "--all", "--metadata-field", "team=nope", "--json"],
            ],
        ),
        // HIGH: labels — add, show, filter.
        Scenario::new(
            "labels",
            "l",
            &[
                &["create", "Tagged", "--id", "l-1", "--force", "-t", "task", "--labels", "urgent,backend", "--json"],
                &["create", "Plain", "--id", "l-2", "--force", "-t", "task", "--json"],
                &["show", "l-1", "--json"],
                &["list", "--all", "--label", "urgent", "--json"],
                &["list", "--all", "--label", "missing", "--json"],
            ],
        ),
        // CRITICAL: parent-child blocking inheritance — a child of a blocked
        // parent should itself be blocked (is_blocked fixpoint).
        Scenario::new(
            "parent_child_block",
            "pc",
            &[
                &["create", "Blocker", "--id", "pc-x", "--force", "-t", "task", "--json"],
                &["create", "Parent", "--id", "pc-p", "--force", "-t", "feature", "--json"],
                &["create", "Child", "--id", "pc-c", "--force", "-t", "task", "--json"],
                &["dep", "add", "pc-c", "pc-p", "--type", "parent-child", "--json"],
                &["dep", "add", "pc-p", "pc-x", "--type", "blocks", "--json"],
                &["ready", "--json"],
                &["close", "pc-x", "--force", "--json"],
                &["ready", "--json"],
            ],
        ),
        // Field coverage: a BLOCKED issue's show omits is_blocked/from/ref/needs
        // (bd never emits these fields).
        Scenario::new(
            "blocked_show_fields",
            "bf",
            &[
                &["create", "A", "--id", "bf-a", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "bf-b", "--force", "-t", "task", "--json"],
                &["dep", "add", "bf-a", "bf-b", "--type", "blocks", "--json"],
                &["show", "bf-a", "--json"],
            ],
        ),
        // Error class: bd sql is unsupported in embedded mode.
        Scenario::new(
            "sql_unsupported_embedded",
            "sq",
            &[&["sql", "SELECT 1", "--json"]],
        ),
        // CRITICAL: transitive blocking — A blocks B blocks C; readiness must
        // propagate as blockers close.
        Scenario::new(
            "transitive_block",
            "tb",
            &[
                &["create", "A", "--id", "tb-a", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "tb-b", "--force", "-t", "task", "--json"],
                &["create", "C", "--id", "tb-c", "--force", "-t", "task", "--json"],
                &["dep", "add", "tb-a", "tb-b", "--type", "blocks", "--json"],
                &["dep", "add", "tb-b", "tb-c", "--type", "blocks", "--json"],
                &["ready", "--json"],
                &["close", "tb-c", "--force", "--json"],
                &["ready", "--json"],
                &["close", "tb-b", "--force", "--json"],
                &["ready", "--json"],
            ],
        ),
        // === query-field parity scenarios ===
        // The wisp-tier read path sends `query ephemeral=true`.
        Scenario::new(
            "query_ephemeral_field",
            "qe",
            &[
                &["create", "Durable", "--id", "qe-1", "--force", "-t", "task", "--json"],
                &["create", "Wisp", "--id", "qe-2", "--force", "-t", "task", "--ephemeral", "--json"],
                &["query", "--json", "ephemeral=true"],
            ],
        ),
        // The label-scoped read path sends `query label=<value>`.
        Scenario::new(
            "query_label_field",
            "ql",
            &[
                &["create", "Tagged", "--id", "ql-1", "--force", "-t", "task", "--labels", "order-run:x", "--json"],
                &["create", "Plain", "--id", "ql-2", "--force", "-t", "task", "--json"],
                &["query", "--json", "label=order-run:x"],
            ],
        ),
        // The parent-scoped read path sends `query parent=<id>`.
        Scenario::new(
            "query_parent_field",
            "qp",
            &[
                &["create", "Parent", "--id", "qp-1", "--force", "-t", "epic", "--json"],
                &["create", "Child", "--id", "qp-2", "--force", "-t", "task", "--json"],
                &["dep", "add", "qp-2", "qp-1", "--type", "parent-child", "--json"],
                &["query", "--json", "parent=qp-1"],
            ],
        ),
        // A blocks add whose only reverse path is parent-child is NOT a cycle in bd
        // (cycle traversal walks blocks + conditional-blocks only).
        Scenario::new(
            "cycle_nonblocking_reverse_allowed",
            "cn",
            &[
                &["create", "A", "--id", "cn-a", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "cn-b", "--force", "-t", "task", "--json"],
                &["dep", "add", "cn-b", "cn-a", "--type", "parent-child", "--json"],
                &["dep", "add", "cn-a", "cn-b", "--type", "blocks", "--json"],
                &["ready", "--json"],
            ],
        ),
        // Claiming an OPEN bead already assigned to the claiming actor transitions it
        // to in_progress (+started_at), not a no-op.
        Scenario::new(
            "claim_preassigned_open",
            "cp",
            &[
                &["create", "X", "--id", "cp-1", "--force", "-t", "task", "--assignee", "CI Bot", "--json"],
                &["update", "cp-1", "--claim", "--json"],
                &["show", "cp-1", "--json"],
            ],
        ),
        // Re-close with a new reason is a NO-OP on close_reason: bd keeps the
        // FIRST reason (close is reason-idempotent). The scenario name is historical.
        Scenario::new(
            "close_reclose_overwrites_reason",
            "ci",
            &[
                &["create", "X", "--id", "ci-1", "--force", "-t", "task", "--json"],
                &["close", "ci-1", "-r", "first", "--json"],
                &["close", "ci-1", "-r", "second", "--json"],
                &["show", "ci-1", "--json"],
            ],
        ),
        // bd's ready excludes a FUTURE-deferred issue even when its status is `open`
        // (reachable via update --status open on a deferred bead). Callers trust ready
        // as the claimable set and never re-check defer.
        Scenario::new(
            "ready_excludes_future_deferred_open",
            "df",
            &[
                &["create", "Deferred", "--id", "df-1", "--force", "-t", "task", "--defer", "2099-01-01", "--json"],
                &["create", "Normal", "--id", "df-2", "--force", "-t", "task", "--json"],
                &["update", "df-1", "--status", "open", "--json"],
                &["ready", "--json"],
            ],
        ),
        // THE CONFIRMED BUG, pinned: bd's ready hides the infra/coordination types
        // (ReadyWorkExcludeTypes: merge-request, gate, molecule, rig, agent, role,
        // message). A task + gate + molecule + message yields ONLY the task as ready
        // claimable work — gate/molecule/message must never surface as claimable.
        Scenario::new(
            "ready_excludes_infra_and_coordination_types",
            "rx",
            &[
                &["create", "Real work", "--id", "rx-task", "--force", "-t", "task", "--json"],
                &["create", "A gate", "--id", "rx-gate", "--force", "-t", "gate", "--json"],
                &["create", "A molecule", "--id", "rx-mol", "--force", "-t", "molecule", "--json"],
                &["create", "A message", "--id", "rx-msg", "--force", "-t", "message", "--json"],
                &["ready", "--json"],
            ],
        ),
        // Companion to the ready exclusion: bd's default `list` hides gate (no
        // --include-gates) and infra types (message; no --include-infra), so the
        // same task+gate+molecule+message set lists as [task, molecule] — molecule
        // is a durable, non-gate, non-infra type that DOES list, while gate and the
        // infra `message` are filtered out. `count` excludes only the wisp tier
        // (infra `message` is auto-ephemeral) and DOES count gate, so count == 3
        // (task + gate + molecule).
        Scenario::new(
            "list_excludes_gate_and_infra_types",
            "lx",
            &[
                &["create", "Real work", "--id", "lx-task", "--force", "-t", "task", "--json"],
                &["create", "A gate", "--id", "lx-gate", "--force", "-t", "gate", "--json"],
                &["create", "A molecule", "--id", "lx-mol", "--force", "-t", "molecule", "--json"],
                &["create", "A message", "--id", "lx-msg", "--force", "-t", "message", "--json"],
                &["list", "--json"],
                &["count", "--json"],
            ],
        ),
        // RT-HIGH #1: a waits-for waiter whose spawner has NO open parent-child child
        // is NOT blocked (bd's default waits-for gate). The old impl lumped waits-for
        // with `blocks` and wrongly withheld the waiter from `ready`. In-scope: uses
        // only `dep add --type waits-for` + `ready --json` (no out-of-scope `blocked`).
        Scenario::new(
            "waits_for_no_children_ready",
            "wf",
            &[
                &["create", "Waiter", "--id", "wf-w", "--force", "-t", "task", "--json"],
                &["create", "Spawner", "--id", "wf-s", "--force", "-t", "task", "--json"],
                &["dep", "add", "wf-w", "wf-s", "--type", "waits-for", "--json"],
                &["ready", "--json"],
            ],
        ),
        // RT-HIGH #1: with an OPEN parent-child child the spawner gates the waiter
        // (waiter blocked, omitted from ready); closing the child opens the gate and
        // the waiter becomes ready. Exercises the incremental re-seed of a waiter when
        // a spawner's child changes status.
        Scenario::new(
            "waits_for_open_child_then_close_ready",
            "wf2",
            &[
                &["create", "Waiter", "--id", "wf2-w", "--force", "-t", "task", "--json"],
                &["create", "Spawner", "--id", "wf2-s", "--force", "-t", "task", "--json"],
                &["create", "Child", "--id", "wf2-c", "--force", "-t", "task", "--json"],
                &["dep", "add", "wf2-c", "wf2-s", "--type", "parent-child", "--json"],
                &["dep", "add", "wf2-w", "wf2-s", "--type", "waits-for", "--json"],
                &["ready", "--json"],
                &["close", "wf2-c", "--force", "--json"],
                &["ready", "--json"],
            ],
        ),
        // DIVERGENCE PIN: `ready --claim` must force Unassigned (never steal an open
        // bead assigned to another agent). rcp-1 (assignee bob, P0) outranks rcp-2
        // (unassigned, P1); bd skips rcp-1 and claims rcp-2. Without the Unassigned
        // force, a claimer takes the higher-priority rcp-1 — cross-agent corruption.
        Scenario::new(
            "ready_claim_skips_preassigned",
            "rcp",
            &[
                &["create", "Assigned", "--id", "rcp-1", "--force", "-t", "task", "-p", "0", "--assignee", "bob", "--json"],
                &["create", "Free", "--id", "rcp-2", "--force", "-t", "task", "-p", "1", "--json"],
                &["ready", "--claim", "--json"],
                &["show", "rcp-1", "--json"],
                &["show", "rcp-2", "--json"],
            ],
        ),
        // DIVERGENCE PIN: `parent` omitempty boundary. bd marshals create/update/close/
        // reopen from raw *types.Issue (NO parent tag) but show/list/ready from the
        // IssueWithCounts/IssueDetails wrappers (parent present). So a parent-child child
        // shows `parent` ONLY under show — not update/close/reopen.
        Scenario::new(
            "output_parent_omitempty_boundary",
            "upo",
            &[
                &["create", "Parent", "--id", "upo-1", "--force", "-t", "epic", "--json"],
                &["create", "Child", "--id", "upo-2", "--force", "-t", "task", "--json"],
                &["dep", "add", "upo-2", "upo-1", "--type", "parent-child", "--json"],
                &["update", "upo-2", "--status", "in_progress", "--json"],
                &["show", "upo-2", "--json"],
                &["close", "upo-2", "--force", "--json"],
                &["reopen", "upo-2", "--json"],
            ],
        ),
        // DIVERGENCE PIN: nested dependency sub-objects in `show` are bare types.Issue
        // (NO parent tag) — even when the target IS a parent-child child. sdp-2 has a
        // parent (sdp-3) but must NOT carry `parent` inside sdp-1's dependencies[].
        Scenario::new(
            "show_dep_subobject_no_parent",
            "sdp",
            &[
                &["create", "Main", "--id", "sdp-1", "--force", "-t", "task", "--json"],
                &["create", "Target", "--id", "sdp-2", "--force", "-t", "task", "--json"],
                &["create", "GrandParent", "--id", "sdp-3", "--force", "-t", "epic", "--json"],
                &["dep", "add", "sdp-2", "sdp-3", "--type", "parent-child", "--json"],
                &["dep", "add", "sdp-1", "sdp-2", "--type", "blocks", "--json"],
                &["show", "sdp-1", "--json"],
            ],
        ),
        // DIVERGENCE PIN: --set-metadata key validation. bd ValidateMetadataKey rejects
        // keys with hyphens/spaces/leading-digits (^[a-zA-Z_][a-zA-Z0-9_.]*$).
        Scenario::new(
            "update_set_metadata_key_validation",
            "umk",
            &[
                &["create", "Base", "--id", "umk-1", "--force", "-t", "task", "--json"],
                &["update", "umk-1", "--set-metadata", "routed-to=x", "--json"],
            ],
        ),
        // DIVERGENCE PIN: --set-metadata scalar coercion. toJSONValue no longer infers
        // JSON types from content — every token, including numeric- and bool-looking
        // ones, is stored as a JSON string (GH#4146). "007", "42", "1.", and "true" all
        // round-trip as strings; use --metadata with a raw JSON blob for explicit
        // non-string types.
        Scenario::new(
            "update_set_metadata_raw_scalars",
            "umc",
            &[
                &["create", "Base", "--id", "umc-1", "--force", "-t", "task", "--json"],
                &["update", "umc-1", "--set-metadata", "a=007", "--set-metadata", "n=42", "--set-metadata", "d=1.", "--set-metadata", "b=true", "--json"],
                &["show", "umc-1", "--json"],
            ],
        ),
        // REGRESSION GUARD: query --limit 0 = UNLIMITED. A regression to
        // unconditional truncate would silently drop matches yet still pass the
        // rest of the suite; pin it.
        Scenario::new(
            "query_limit_zero_unlimited",
            "qlz",
            &[
                &["create", "A", "--id", "qlz-1", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "qlz-2", "--force", "-t", "task", "--json"],
                &["create", "C", "--id", "qlz-3", "--force", "-t", "task", "--json"],
                &["query", "status=open", "--limit", "0", "--json"],
            ],
        ),
        // DIVERGENCE PIN: dep remove resolves source (then target) and HARD-ERRORS on a
        // miss (exit 1), not a silent no-op. Target miss uses the doubled resolving string.
        Scenario::new(
            "dep_remove_missing_resolution",
            "drm",
            &[
                &["create", "A", "--id", "drm-1", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "drm-2", "--force", "-t", "task", "--json"],
                &["dep", "add", "drm-1", "drm-2", "--type", "blocks", "--json"],
                &["dep", "remove", "drm-999", "drm-2", "--json"],
                &["dep", "remove", "drm-1", "drm-999", "--json"],
            ],
        ),
        // DIVERGENCE PIN: a PINNED blocker is excluded from the close-guard (active :=
        // not closed AND not pinned), so a target blocked only by a pinned issue closes.
        Scenario::new(
            "close_pinned_blocker_closes",
            "cpb",
            &[
                &["create", "Blocker", "--id", "cpb-1", "--force", "-t", "task", "--json"],
                &["create", "Target", "--id", "cpb-2", "--force", "-t", "task", "--json"],
                &["dep", "add", "cpb-2", "cpb-1", "--type", "blocks", "--json"],
                &["update", "cpb-1", "--status", "pinned", "--json"],
                &["close", "cpb-2", "--json"],
            ],
        ),
        // DIVERGENCE PIN: non-`blocks` blocker types render with a ` (<type>)` suffix in
        // the close-guard message.
        Scenario::new(
            "close_conditional_blocks_suffix",
            "ccs",
            &[
                &["create", "Blocker", "--id", "ccs-1", "--force", "-t", "task", "--json"],
                &["create", "Target", "--id", "ccs-2", "--force", "-t", "task", "--json"],
                &["dep", "add", "ccs-2", "ccs-1", "--type", "conditional-blocks", "--json"],
                &["close", "ccs-2", "--json"],
            ],
        ),
        // DIVERGENCE PIN: empty `--reason ""` values are stripped before mapping, so
        // `-r "" -r keep` is one shared reason `keep` and a lone `-r ""` -> default "Closed".
        Scenario::new(
            "close_empty_reason_stripped",
            "cer",
            &[
                &["create", "A", "--id", "cer-1", "--force", "-t", "task", "--json"],
                &["create", "B", "--id", "cer-2", "--force", "-t", "task", "--json"],
                &["create", "C", "--id", "cer-3", "--force", "-t", "task", "--json"],
                &["close", "cer-1", "cer-2", "-r", "", "-r", "keep", "--json"],
                &["close", "cer-3", "-r", "", "--json"],
            ],
        ),
        // DIVERGENCE PIN: `list --parent` is a two-disjunct OR — a parent-child edge OR a
        // dotted child id with no edge. The orphan dotted ldo-1.2 must appear under ldo-1.
        Scenario::new(
            "list_parent_dotted_orphan",
            "ldo",
            &[
                &["create", "Parent", "--id", "ldo-1", "--force", "-t", "epic", "--json"],
                // disjunct 1: a non-dotted child reached via a real parent-child edge.
                &["create", "EdgeChild", "--id", "ldo-2", "--force", "-t", "task", "--json"],
                &["dep", "add", "ldo-2", "ldo-1", "--type", "parent-child", "--json"],
                // disjunct 2: a dotted child id with NO edge (orphan).
                &["create", "OrphanDotted", "--id", "ldo-1.1", "--force", "-t", "task", "--json"],
                &["list", "--parent", "ldo-1", "--json"],
            ],
        ),
        // DIVERGENCE PIN: purge reports ZERO child-row metrics (deps/labels/events) for
        // its all-ephemeral candidate set, even with a real edge + label present.
        Scenario::new(
            "purge_dry_run_zero_metrics",
            "pdr",
            &[
                &["create", "E1", "--id", "pdr-1", "--ephemeral", "-t", "task", "-l", "red", "--json"],
                &["create", "E2", "--id", "pdr-2", "--ephemeral", "-t", "task", "--json"],
                &["dep", "add", "pdr-1", "pdr-2", "--type", "blocks", "--json"],
                &["close", "pdr-1", "--force", "--json"],
                &["close", "pdr-2", "--force", "--json"],
                &["purge", "--dry-run", "--json"],
            ],
        ),
        // DIVERGENCE PIN: an exact HASH match wins unambiguously over substring co-matches.
        // `show abc` resolves sh-abc even though sh-abc2 also contains "abc".
        Scenario::new(
            "id_exact_hash_resolves",
            "sh",
            &[
                &["create", "One", "--id", "sh-abc", "--force", "-t", "task", "--json"],
                &["create", "Two", "--id", "sh-abc2", "--force", "-t", "task", "--json"],
                &["show", "abc", "--json"],
            ],
        ),
        // DIVERGENCE PIN: `config set` rejects protected/sql-server-only keys (exit 1)
        // before writing — `issue-prefix`/`issue_prefix` and `dolt.debug` (embedded).
        Scenario::new(
            "config_set_protected_keys",
            "cfg",
            &[
                &["config", "set", "issue-prefix", "foo", "--json"],
                &["config", "set", "dolt.debug", "true", "--json"],
            ],
        ),
        // COVERAGE: delete recomputes the surviving neighbour's is_blocked. del-b is
        // blocked by del-a (omitted from ready); deleting del-a must make del-b ready.
        // delete is a top-danger op — it must recompute surviving neighbours.
        Scenario::new(
            "delete_unblocks_neighbour",
            "del",
            &[
                &["create", "Blocker", "--id", "del-a", "--force", "-t", "task", "--json"],
                &["create", "Blocked", "--id", "del-b", "--force", "-t", "task", "--json"],
                &["dep", "add", "del-b", "del-a", "--type", "blocks", "--json"],
                &["ready", "--json"],
                &["delete", "del-a", "--force", "--json"],
                &["ready", "--json"],
            ],
        ),
        // COVERAGE: comment add (bd comment <id> <text>) then list (bd comments <id>).
        // Comment id (UUID) and created_at (TS) are normalized, so the trace is stable.
        // The rig had ZERO comment steps before this.
        Scenario::new(
            "comment_add_list",
            "cm",
            &[
                &["create", "Base", "--id", "cm-1", "--force", "-t", "task", "--json"],
                &["comment", "cm-1", "first note", "--json"],
                &["comment", "cm-1", "second note", "--json"],
                &["comments", "cm-1", "--json"],
            ],
        ),
        // COVERAGE: config set/get SUCCESS path (the protected-keys scenario only pins
        // the reject path). custom.* keys are user-defined and warning-free. get echoes
        // the value set; get of a missing key returns an empty value, not an error.
        Scenario::new(
            "config_set_get_success",
            "cfs",
            &[
                &["config", "set", "custom.team", "core", "--json"],
                &["config", "get", "custom.team", "--json"],
                &["config", "get", "custom.absent", "--json"],
            ],
        ),
        // COVERAGE: a REAL (non-dry-run) purge that mutates, plus the re-seed that
        // must follow it. Two closed ephemerals are purged (purged_count=2), then a
        // fresh create + list proves the workspace is usable after the purge.
        Scenario::new(
            "purge_real_then_reseed",
            "prg",
            &[
                &["create", "E1", "--id", "prg-1", "--ephemeral", "-t", "task", "--json"],
                &["create", "E2", "--id", "prg-2", "--ephemeral", "-t", "task", "--json"],
                &["close", "prg-1", "--force", "--json"],
                &["close", "prg-2", "--force", "--json"],
                &["purge", "--force", "--json"],
                &["create", "Fresh", "--id", "prg-3", "--force", "-t", "task", "--json"],
                &["list", "--all", "--json"],
            ],
        ),
        // DIVERGENCE PIN (review gap): `--force` close of a PINNED issue must
        // actually close it. The close-guard treats pinned as "not active" for blockers,
        // but the TARGET being pinned is a different axis — force must still transition
        // the pinned bead itself to closed. The corpus pinned pinned-as-blocker
        // (close_pinned_blocker_closes) but never force-closing a pinned target.
        Scenario::new(
            "close_pinned_force",
            "cpf",
            &[
                &["create", "Pinned", "--id", "cpf-1", "--force", "-t", "task", "-p", "2", "--json"],
                &["update", "cpf-1", "--status", "pinned", "--json"],
                &["close", "cpf-1", "--force", "--json"],
                &["show", "cpf-1", "--json"],
            ],
        ),
        // OUT-OF-SCOPE for the gate (bd `note` is not in scoreboard IN_SCOPE_CMDS) but
        // captured informationally (review gap): two `note` appends then `show`
        // pins the stored notes bytes + the separator bd inserts between appended notes.
        // The corpus pinned `comment`/`comments` but never the distinct `note` append path.
        Scenario::new(
            "note_append_two",
            "nt",
            &[
                &["create", "Base", "--id", "nt-1", "--force", "-t", "task", "--json"],
                &["note", "nt-1", "first note", "--json"],
                &["note", "nt-1", "second note", "--json"],
                &["show", "nt-1", "--json"],
            ],
        ),
    ]
}
