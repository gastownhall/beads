//! Live differential conformance: run the SAME command sequence against a
//! reference `bd` (the "before") and a candidate `bd` (the "after") in isolated
//! workspaces, then diff their observable behavior — stdout, stderr, and exit
//! code, per step.
//!
//! Real behavioral parity can only be established by running both binaries and
//! comparing — including multi-issue ordering, close→ready transitions, claim
//! lifecycle, storage tiers, cycles, and exit/stream behavior.
//!
//! Comparison is JSON-aware: object key order is ignored (cosmetic), but ARRAY
//! order is significant where a scenario is marked `ordered`.

use serde::{Deserialize, Serialize};
use std::path::Path;
use std::process::Command;
use std::sync::OnceLock;

/// The actor identity bd derives from git config on this host. Both runners use
/// the same one; we normalize it to a stable token so traces are host-independent.
pub const ACTOR_NAME: &str = "CI Bot";
pub const ACTOR_EMAIL: &str = "ci@beads.test";

/// A scenario: a prefix to `bd init` with, and an ordered list of argv steps
/// (each run after init in the same workspace).
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Scenario {
    pub name: String,
    pub prefix: String,
    pub steps: Vec<Vec<String>>,
    /// When true, top-level array stdout is compared as an ORDERED sequence, not a
    /// multiset. Only valid for scenarios whose bd order is implementation-independent
    /// (all-distinct priorities) — equal-priority/same-second order is timestamp-precision
    /// dependent and must stay a multiset (see `stdout_equiv`).
    #[serde(default)]
    pub ordered: bool,
}

impl Scenario {
    pub fn new(name: &str, prefix: &str, steps: &[&[&str]]) -> Self {
        Scenario {
            name: name.to_string(),
            prefix: prefix.to_string(),
            steps: steps
                .iter()
                .map(|s| s.iter().map(|a| a.to_string()).collect())
                .collect(),
            ordered: false,
        }
    }

    /// Mark this scenario's array output as order-sensitive (see `Scenario::ordered`).
    pub fn ordered(mut self) -> Self {
        self.ordered = true;
        self
    }
}

/// The captured, normalized result of one step.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct StepResult {
    pub args: Vec<String>,
    pub exit: i32,
    pub stdout: String,
    pub stderr: String,
}

/// A full normalized run of a scenario against one binary.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct Trace {
    pub scenario: String,
    pub steps: Vec<StepResult>,
}

fn ts_regex() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| {
        // RFC3339 with optional sub-second precision and Z or numeric offset.
        regex::Regex::new(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})")
            .expect("valid timestamp regex")
    })
}

fn uuid_regex() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| {
        // 8-4-4-4-12 hex UUID (e.g. comment ids) — random per run, not verifiable.
        regex::Regex::new(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
            .expect("valid uuid regex")
    })
}

/// Replace volatile values (timestamps, host identity, random UUIDs) with stable
/// tokens. UUIDs (e.g. comment ids) are random per run, so byte-diffing them is
/// meaningless — collapse them like timestamps.
pub fn normalize(s: &str) -> String {
    let s = ts_regex().replace_all(s, "<TS>").into_owned();
    let s = uuid_regex().replace_all(&s, "<UUID>").into_owned();
    s.replace(ACTOR_EMAIL, "<EMAIL>").replace(ACTOR_NAME, "<ACTOR>")
}

/// Run a scenario against `bin` in a throwaway workspace, returning the
/// normalized trace. Requires the binary to support `init -p <prefix> --quiet`
/// and to honor the workspace cwd (real bd uses embedded Dolt under `.beads/`).
pub fn run_scenario(bin: &Path, sc: &Scenario) -> std::io::Result<Trace> {
    let work = tempfile::tempdir()?;
    let run = |args: &[String]| -> std::io::Result<std::process::Output> {
        // Scrub the environment to an explicit whitelist. The upstream harness
        // inherited the FULL host env, so any host BEADS_*/BD_* var
        // (BEADS_DOLT_SERVER_MODE, BEADS_ACTOR, auto-export toggles) silently
        // reshaped what BOTH binaries do — symmetric, so no divergence, but the
        // gate then certifies a different configuration than users run. Passing a
        // deliberate env makes each green attributable to the same config every
        // run and on any host. (BEADS_TEST_MODE=1 is retained deliberately; its
        // store-construction delta — auto-server decisions off, port rewiring —
        // is a documented ungated gap: this rig does not cover mode/topology
        // construction paths. See tests/oracle-a/README.md.)
        let mut cmd = Command::new(bin);
        cmd.args(args)
            .current_dir(work.path())
            .env_clear()
            .env("BEADS_TEST_MODE", "1");
        for key in ["PATH", "HOME", "TMPDIR"] {
            if let Ok(val) = std::env::var(key) {
                cmd.env(key, val);
            }
        }
        cmd.output()
    };

    // init (output discarded; banners are not part of the contract)
    let init = vec![
        "init".to_string(),
        "-p".to_string(),
        sc.prefix.clone(),
        "--quiet".to_string(),
    ];
    run(&init)?;

    let mut steps = Vec::with_capacity(sc.steps.len());
    for args in &sc.steps {
        let out = run(args)?;
        steps.push(StepResult {
            args: args.clone(),
            exit: out.status.code().unwrap_or(-1),
            stdout: normalize(&String::from_utf8_lossy(&out.stdout)),
            stderr: normalize(&String::from_utf8_lossy(&out.stderr)),
        });
    }
    Ok(Trace {
        scenario: sc.name.clone(),
        steps,
    })
}

/// One observed difference between reference and candidate.
#[derive(Clone, Debug)]
pub struct Divergence {
    pub step: usize,
    pub args: Vec<String>,
    pub field: &'static str,
    pub reference: String,
    pub candidate: String,
}

impl std::fmt::Display for Divergence {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "step {} `{}` {} mismatch:\n   reference: {}\n   candidate: {}",
            self.step,
            self.args.join(" "),
            self.field,
            self.reference.trim(),
            self.candidate.trim()
        )
    }
}

/// JSON-aware stdout equivalence. Object key order is ignored (cosmetic).
///
/// Array order is compared as a MULTISET, not a sequence. Empirically, bd's
/// ready/list ordering among equal-priority issues is wall-clock +
/// timestamp-precision dependent: bd's
/// slow creates land in distinct seconds (ordering by `created_at`), while a
/// fast reimplementation ties them in one second (ordering by `id`). Since
/// `created_at` is itself normalized to `<TS>`, the order is neither stable nor
/// verifiable across implementations — so by default we verify the SET, fields, and
/// counts (all reproducible) and do NOT assert sequence order.
///
/// Scenarios with all-DISTINCT priorities are the exception: `priority ASC` fully
/// determines the order independent of timestamp precision, so those are marked
/// `Scenario::ordered` and compared as a sequence here — the dedicated order-parity
/// check that closes the multiset blind spot.
fn stdout_equiv(a: &str, b: &str, ordered: bool) -> bool {
    match (
        serde_json::from_str::<serde_json::Value>(a),
        serde_json::from_str::<serde_json::Value>(b),
    ) {
        (Ok(va), Ok(vb)) if ordered => values_equiv_ordered(&va, &vb),
        (Ok(va), Ok(vb)) => values_equiv(&va, &vb),
        _ => a == b,
    }
}

/// Like `values_equiv` but arrays are compared as an ORDERED sequence (objects stay
/// key-order-insensitive). Used only for `Scenario::ordered` scenarios.
fn values_equiv_ordered(a: &serde_json::Value, b: &serde_json::Value) -> bool {
    use serde_json::Value;
    match (a, b) {
        (Value::Array(xa), Value::Array(xb)) => {
            xa.len() == xb.len() && xa.iter().zip(xb).all(|(ea, eb)| values_equiv_ordered(ea, eb))
        }
        (Value::Object(oa), Value::Object(ob)) => {
            oa.len() == ob.len()
                && oa.iter().all(|(k, va)| ob.get(k).is_some_and(|vb| values_equiv_ordered(va, vb)))
        }
        _ => a == b,
    }
}

/// Structural equivalence treating arrays as multisets (order-insensitive) and
/// objects as key-order-insensitive.
fn values_equiv(a: &serde_json::Value, b: &serde_json::Value) -> bool {
    use serde_json::Value;
    match (a, b) {
        (Value::Array(xa), Value::Array(xb)) => {
            if xa.len() != xb.len() {
                return false;
            }
            let mut used = vec![false; xb.len()];
            'next: for ea in xa {
                for (i, eb) in xb.iter().enumerate() {
                    if !used[i] && values_equiv(ea, eb) {
                        used[i] = true;
                        continue 'next;
                    }
                }
                return false;
            }
            true
        }
        (Value::Object(oa), Value::Object(ob)) => {
            oa.len() == ob.len()
                && oa
                    .iter()
                    .all(|(k, va)| ob.get(k).is_some_and(|vb| values_equiv(va, vb)))
        }
        _ => a == b,
    }
}

/// Diff two traces step-by-step. Reference and candidate must have run the same
/// scenario. Returns every observed divergence (empty == behavioral parity for
/// this scenario, modulo normalized volatiles).
pub fn diff(reference: &Trace, candidate: &Trace, ordered: bool) -> Vec<Divergence> {
    let mut out = Vec::new();
    let n = reference.steps.len().min(candidate.steps.len());
    for i in 0..n {
        let r = &reference.steps[i];
        let c = &candidate.steps[i];
        if r.exit != c.exit {
            out.push(Divergence {
                step: i,
                args: r.args.clone(),
                field: "exit code",
                reference: r.exit.to_string(),
                candidate: c.exit.to_string(),
            });
        }
        if !stdout_equiv(&r.stdout, &c.stdout, ordered) {
            out.push(Divergence {
                step: i,
                args: r.args.clone(),
                field: "stdout",
                reference: r.stdout.clone(),
                candidate: c.stdout.clone(),
            });
        }
        if r.stderr.trim() != c.stderr.trim() {
            out.push(Divergence {
                step: i,
                args: r.args.clone(),
                field: "stderr",
                reference: r.stderr.clone(),
                candidate: c.stderr.clone(),
            });
        }
    }
    if reference.steps.len() != candidate.steps.len() {
        out.push(Divergence {
            step: n,
            args: vec![],
            field: "step count",
            reference: reference.steps.len().to_string(),
            candidate: candidate.steps.len().to_string(),
        });
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ordered_comparison_bites_on_permutations_multiset_does_not() {
        // A reordered array is EQUAL as a multiset but UNEQUAL as a sequence — this is
        // exactly the blind spot `Scenario::ordered` closes.
        let a = r#"[{"id":"x-0"},{"id":"x-1"},{"id":"x-2"}]"#;
        let permuted = r#"[{"id":"x-1"},{"id":"x-0"},{"id":"x-2"}]"#;
        assert!(stdout_equiv(a, permuted, false), "multiset must accept a permutation");
        assert!(!stdout_equiv(a, permuted, true), "ordered must REJECT a permutation");
        // Identical order passes both; object key order is still ignored under ordered.
        let reordered_keys = r#"[{"id":"x-0"},{"id":"x-1"},{"id":"x-2"}]"#;
        assert!(stdout_equiv(a, reordered_keys, true));
    }

    #[test]
    fn normalize_collapses_timestamps_and_identity() {
        let s = r#"{"created_at":"2026-06-27T00:02:33.222549155Z","owner":"ci@beads.test","by":"CI Bot"}"#;
        let n = normalize(s);
        assert!(n.contains("<TS>"));
        assert!(n.contains("<EMAIL>"));
        assert!(n.contains("<ACTOR>"));
        assert!(!n.contains("2026"));
    }

    #[test]
    fn stdout_equiv_ignores_key_and_array_order_but_catches_set_diffs() {
        // key order ignored
        assert!(stdout_equiv(r#"{"a":1,"b":2}"#, r#"{"b":2,"a":1}"#, false));
        // array order ignored (multiset) — bd's timing-dependent order is not pinned
        assert!(stdout_equiv(r#"[{"id":"a"},{"id":"b"}]"#, r#"[{"id":"b"},{"id":"a"}]"#, false));
        // but a genuinely different set IS caught
        assert!(!stdout_equiv(r#"[{"id":"a"}]"#, r#"[{"id":"b"}]"#, false));
        // and multiset cardinality IS caught
        assert!(!stdout_equiv(r#"[{"id":"a"},{"id":"a"}]"#, r#"[{"id":"a"}]"#, false));
    }
}
