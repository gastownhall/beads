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

/// The `BEADS_TIP_SEED` every step runs with. After a successful human-output
/// `create`/`show`/`list`/`ready`, bd may append a "💡 Tip:" line chosen by a
/// probability roll, and unless BEADS_TIP_SEED is set that RNG is seeded from
/// the clock (cmd/bd/tips.go `initTipRand`), so the same binary prints the tip
/// on a different step, or not at all, from run to run. For this seed Go's
/// `math/rand` returns 0.0000159 as its first `Float64()`, so the
/// highest-priority eligible tip always wins its roll. Which tip prints, and on
/// which step, then depends only on eligibility, priority and the tip's
/// frequency gate, so any change to which tip prints, on which step, or to its
/// text still diverges instead of being normalized away. (A tip only prints
/// where the host makes it eligible; see tests/oracle-a/README.md.)
pub const TIP_SEED: &str = "1214";

/// A scenario: a prefix to `bd init` with, and an ordered list of argv steps
/// (each run after init in the same workspace). A scenario whose first step is
/// its own `init` owns initialization: it starts in an empty workspace and
/// `prefix` is unused (see `Scenario::owns_init`).
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

    /// True when the first step is the scenario's own `bd init`. The runner then
    /// skips its `init -p <prefix> --quiet`, so that step initializes an empty
    /// workspace instead of hitting "already initialized". Only the first step
    /// counts: an `init` later in a scenario is a re-init by design. The match is
    /// on the first token, like the scoreboard's in-scope predicate, so a first
    /// step with leading global flags (`-q init ...`) is not recognized; and a
    /// first-step init that creates no workspace (`init --help`, or one that
    /// fails flag validation) leaves any later steps in an empty workspace.
    pub fn owns_init(&self) -> bool {
        self.steps
            .first()
            .and_then(|step| step.first())
            .is_some_and(|cmd| cmd == "init")
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

fn revision_regex() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| {
        // The `"revision": "<token>"` JSON member (types.IssueDetails.Revision, on
        // `bd show --json` since #5535). The token is the row's row_lock, which
        // every create, update, close, reopen, claim and unclaim remints from
        // crypto/rand (issueops.freshRowLock), so its value differs on every run.
        // Only a NON-ZERO decimal token is collapsed: "0" (a never-stamped row)
        // stays literal, and the member's name, presence and string type are
        // untouched.
        //
        // The match is by member NAME over the raw text, before any JSON parse,
        // so it collapses EVERY `"revision"` member holding such a string, at any
        // depth and in plain (non-JSON) text too, not only the issue token. That
        // includes a user metadata key named `revision`, which the in-scope
        // --set-metadata/--metadata flags can set: a change to its value would
        // pass the gate. No scenario sets one; use another key name in any that
        // would. Pinned by normalize_collapses_revision_by_member_name_at_any_depth.
        regex::Regex::new(r#"("revision"\s*:\s*")-?[1-9][0-9]*(")"#).expect("valid revision regex")
    })
}

fn repo_id_regex() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| {
        // `bd init`'s "Repository ID: <8 hex>" line (cmd/bd/init.go). With no
        // origin remote the id hashes the repo root path, so it changes with the
        // run's random workspace. Only that label at that width is collapsed.
        regex::Regex::new(r"(Repository ID: )[0-9a-f]{8}\b").expect("valid repository id regex")
    })
}

fn clone_id_regex() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| {
        // `bd init`'s "Clone ID: <16 hex>" line: a hash of the hostname and the
        // repo root path, so it too changes with the run's random workspace.
        regex::Regex::new(r"(Clone ID: )[0-9a-f]{16}\b").expect("valid clone id regex")
    })
}

/// Replace volatile values (timestamps, host identity, random UUIDs, revision
/// tokens, `bd init` fingerprints) with stable tokens. UUIDs (e.g. comment ids)
/// and revision tokens are random per run, so byte-diffing them is meaningless —
/// collapse them like timestamps.
pub fn normalize(s: &str) -> String {
    let s = revision_regex()
        .replace_all(s, "${1}<REV>${2}")
        .into_owned();
    let s = repo_id_regex()
        .replace_all(&s, "${1}<REPO_ID>")
        .into_owned();
    let s = clone_id_regex()
        .replace_all(&s, "${1}<CLONE_ID>")
        .into_owned();
    let s = ts_regex().replace_all(&s, "<TS>").into_owned();
    let s = uuid_regex().replace_all(&s, "<UUID>").into_owned();
    s.replace(ACTOR_EMAIL, "<EMAIL>").replace(ACTOR_NAME, "<ACTOR>")
}

/// `normalize`, after first collapsing this run's own workspace directory to
/// `<WORKSPACE>`. Each run gets a fresh random tempdir, so any output that
/// echoes it (e.g. the `File: <dir>/CLAUDE.md` lines a scenario's own `init`
/// step prints) differs on every run. Only these exact path strings are
/// replaced, longest first, so a spelling that contains another
/// (`/private/var/...` around `/var/...`) is replaced whole.
pub fn normalize_in_workspace(s: &str, workspace: &[String]) -> String {
    let mut paths: Vec<&str> = workspace
        .iter()
        .map(String::as_str)
        .filter(|p| !p.is_empty())
        .collect();
    paths.sort_by_key(|p| std::cmp::Reverse(p.len()));
    let mut s = s.to_string();
    for p in paths {
        s = s.replace(p, "<WORKSPACE>");
    }
    normalize(&s)
}

/// The argv of the harness's own `init -p <prefix> --quiet`, run before the
/// scenario's steps, or `None` when the scenario owns its init: running both
/// made that first step a double init that failed "already initialized"
/// (unless the step's flag validation failed first).
fn harness_init(sc: &Scenario) -> Option<Vec<String>> {
    (!sc.owns_init()).then(|| {
        vec![
            "init".to_string(),
            "-p".to_string(),
            sc.prefix.clone(),
            "--quiet".to_string(),
        ]
    })
}

/// Run a scenario against `bin` in a throwaway workspace, returning the
/// normalized trace. Requires the binary to support `init -p <prefix> --quiet`
/// and to honor the workspace cwd (real bd uses embedded Dolt under `.beads/`).
pub fn run_scenario(bin: &Path, sc: &Scenario) -> std::io::Result<Trace> {
    let work = tempfile::tempdir()?;
    // Every spelling of the workspace bd may print: the tempdir as created and
    // its symlink-resolved form (macOS tempdirs sit under /var -> /private/var).
    let mut workspace = vec![work.path().to_string_lossy().into_owned()];
    if let Ok(real) = work.path().canonicalize() {
        workspace.push(real.to_string_lossy().into_owned());
    }
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
        // construction paths. See tests/oracle-a/README.md.) BEADS_TIP_SEED pins
        // bd's tip roll; see TIP_SEED.
        let mut cmd = Command::new(bin);
        cmd.args(args)
            .current_dir(work.path())
            .env_clear()
            .env("BEADS_TEST_MODE", "1")
            .env("BEADS_TIP_SEED", TIP_SEED);
        for key in ["PATH", "HOME", "TMPDIR"] {
            if let Ok(val) = std::env::var(key) {
                cmd.env(key, val);
            }
        }
        cmd.output()
    };

    run_steps(sc, &workspace, run)
}

/// The scenario's command sequence: the harness's own init when the scenario
/// does not own one (see `harness_init`), then each step, normalized against
/// `workspace`. `run` executes one argv; it is a parameter so the wiring can be
/// tested without spawning a process.
fn run_steps(
    sc: &Scenario,
    workspace: &[String],
    mut run: impl FnMut(&[String]) -> std::io::Result<std::process::Output>,
) -> std::io::Result<Trace> {
    // init (output discarded; banners are not part of the contract), unless the
    // scenario's first step is its own init.
    if let Some(init) = harness_init(sc) {
        run(&init)?;
    }

    let mut steps = Vec::with_capacity(sc.steps.len());
    for args in &sc.steps {
        let out = run(args)?;
        steps.push(StepResult {
            args: args.clone(),
            exit: out.status.code().unwrap_or(-1),
            stdout: normalize_in_workspace(&String::from_utf8_lossy(&out.stdout), workspace),
            stderr: normalize_in_workspace(&String::from_utf8_lossy(&out.stderr), workspace),
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

    /// Serializes the tests that spawn processes through `run_scenario`. One of
    /// them writes a stub bd with `std::fs::write` and then execs it; if another
    /// test forks while that write fd is still open, the child inherits the fd
    /// until it execs, and running the stub fails with ETXTBSY ("Text file
    /// busy", rust-lang/rust#114554). Take this lock first in every such test.
    static SPAWN: std::sync::Mutex<()> = std::sync::Mutex::new(());

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
    fn normalize_collapses_non_zero_revision_strings() {
        // Two runs of the same `show --json`: only the minted token differs.
        let run1 =
            "[\n  {\n    \"id\": \"g-root\",\n    \"revision\": \"-1228122709301203494\"\n  }\n]\n";
        let run2 =
            "[\n  {\n    \"id\": \"g-root\",\n    \"revision\": \"5267907219149755461\"\n  }\n]\n";
        assert_eq!(normalize(run1), normalize(run2));
        assert!(normalize(run1).contains(r#""revision": "<REV>""#));
        assert!(stdout_equiv(&normalize(run1), &normalize(run2), true));
        assert_eq!(normalize(r#"{"revision":"42"}"#), r#"{"revision":"<REV>"}"#);

        // Everything that is NOT a minted token keeps its literal value:
        // "0" (a never-stamped row) must still diverge from a minted token.
        assert_eq!(normalize(r#"{"revision":"0"}"#), r#"{"revision":"0"}"#);
        assert!(!stdout_equiv(
            &normalize(r#"{"revision":"0"}"#),
            &normalize(r#"{"revision":"7"}"#),
            false
        ));
        // a number-typed member (a wire-type change) is not collapsed,
        assert_eq!(normalize(r#"{"revision":42}"#), r#"{"revision":42}"#);
        // nor are other members whose names merely contain "revision",
        assert_eq!(
            normalize(r#"{"local_revision":"3"}"#),
            r#"{"local_revision":"3"}"#
        );
        // nor the same text quoted inside a JSON string value.
        let quoted = r#"{"description":"see \"revision\": \"12\""}"#;
        assert_eq!(normalize(quoted), quoted);

        // Dropping the member is still a divergence.
        assert!(!stdout_equiv(
            &normalize(r#"{"id":"a","revision":"9"}"#),
            &normalize(r#"{"id":"a"}"#),
            false
        ));
    }

    #[test]
    fn normalize_collapses_revision_by_member_name_at_any_depth() {
        // The scope documented on revision_regex: the match is by member name over
        // the raw text, not by position, so a user metadata key named "revision"
        // is collapsed too and a change to its value is NOT a divergence.
        let meta3 = r#"[{"id":"g-1","revision":"11","metadata":{"revision":"3"}}]"#;
        let meta4 = r#"[{"id":"g-1","revision":"22","metadata":{"revision":"4"}}]"#;
        assert_eq!(
            normalize(meta3),
            r#"[{"id":"g-1","revision":"<REV>","metadata":{"revision":"<REV>"}}]"#
        );
        assert!(stdout_equiv(&normalize(meta3), &normalize(meta4), false));
        // Plain (non-JSON) text is matched as well.
        assert_eq!(
            normalize(r#"config "revision": "12" pinned"#),
            r#"config "revision": "<REV>" pinned"#
        );
        // A metadata key with any other name keeps its value, so it still gates.
        assert!(!stdout_equiv(
            &normalize(r#"{"metadata":{"rev":"3"}}"#),
            &normalize(r#"{"metadata":{"rev":"4"}}"#),
            false
        ));
    }

    #[test]
    fn normalize_in_workspace_collapses_only_this_runs_workspace() {
        // Listed short-first: the longer spelling must still be replaced whole.
        let ws = vec![
            "/var/t/.tmpAbC123".to_string(),
            "/private/var/t/.tmpAbC123".to_string(),
        ];
        let err = "Found existing Dolt database: /private/var/t/.tmpAbC123/.beads/embeddeddolt/c\n";
        assert_eq!(
            normalize_in_workspace(err, &ws),
            "Found existing Dolt database: <WORKSPACE>/.beads/embeddeddolt/c\n"
        );
        // Two runs that differ only in their random workspace normalize equal.
        let other = vec!["/var/t/.tmpXyZ789".to_string()];
        assert_eq!(
            normalize_in_workspace("db at /var/t/.tmpAbC123/.beads", &ws),
            normalize_in_workspace("db at /var/t/.tmpXyZ789/.beads", &other)
        );
        // A path that is not this run's workspace stays literal.
        let foreign = "db at /var/t/.tmpXyZ789/.beads";
        assert_eq!(normalize_in_workspace(foreign, &ws), foreign);
        // The volatile-value normalizers still apply.
        assert_eq!(
            normalize_in_workspace(r#"{"revision":"5"}"#, &ws),
            r#"{"revision":"<REV>"}"#
        );
    }

    #[cfg(unix)]
    #[test]
    fn run_scenario_collapses_the_workspace_in_both_step_streams() {
        let _spawn = SPAWN.lock().unwrap_or_else(|e| e.into_inner());
        // A stub bd that echoes its cwd on stdout and its symlink-resolved form
        // on stderr, like the "Found existing Dolt database" error: both must
        // reach the trace as <WORKSPACE>, so the run's random tempdir never does.
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let bin = dir.path().join("bd");
        std::fs::write(
            &bin,
            "#!/bin/sh\npwd\necho \"Found existing Dolt database: $(pwd -P)/.beads/x\" >&2\n",
        )
        .unwrap();
        std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o755)).unwrap();
        let sc = Scenario::new("wiring", "w", &[&["show", "--json"]]);
        let trace = run_scenario(&bin, &sc).unwrap();
        assert_eq!(trace.steps.len(), 1);
        assert_eq!(trace.steps[0].stdout, "<WORKSPACE>\n");
        assert_eq!(
            trace.steps[0].stderr,
            "Found existing Dolt database: <WORKSPACE>/.beads/x\n"
        );
    }

    #[test]
    fn owns_init_only_when_the_first_step_is_init() {
        let owns = |steps: &[&[&str]]| Scenario::new("t", "x", steps).owns_init();
        assert!(owns(&[
            &["init", "--prefix", "x", "--non-interactive"],
            &["list"]
        ]));
        assert!(owns(&[&["init"]]));
        assert!(!owns(&[&["list", "--json"]]));
        assert!(!owns(&[&["create", "A"], &["init", "--prefix", "x"]]));
        assert!(!owns(&[&["config", "get", "init"]]));
        assert!(!owns(&[]));
        assert!(!owns(&[&[]]));
    }

    #[test]
    fn normalize_collapses_init_fingerprints_and_nothing_else() {
        // Two runs of the same `bd init`: only the path-derived ids differ.
        let run1 = "  Repository ID: e992de87\n  Clone ID: f549016337e74819\n";
        let run2 = "  Repository ID: fd0fa98d\n  Clone ID: f4cd421d9411fa0f\n";
        assert_eq!(normalize(run1), normalize(run2));
        assert_eq!(
            normalize(run1),
            "  Repository ID: <REPO_ID>\n  Clone ID: <CLONE_ID>\n"
        );
        // A width change still diverges: neither id is collapsed at another width,
        let long = "  Repository ID: e992de87e992de87\n  Clone ID: f549016337e7\n";
        assert_eq!(normalize(long), long);
        let wide_clone = "  Clone ID: f549016337e74819ab\n";
        assert_eq!(normalize(wide_clone), wide_clone);
        // nor is the same hex under any other label.
        let other = "  Project ID: e992de87\n  clone id: f549016337e74819\n";
        assert_eq!(normalize(other), other);
    }

    #[test]
    fn harness_init_is_skipped_only_for_a_scenario_whose_first_step_is_init() {
        let init = |steps: &[&[&str]]| harness_init(&Scenario::new("t", "x", steps));
        let pre_init = Some(vec![
            "init".to_string(),
            "-p".to_string(),
            "x".to_string(),
            "--quiet".to_string(),
        ]);
        // A scenario that owns its init starts from an empty workspace, so its
        // init runs for real instead of hitting "already initialized".
        assert_eq!(init(&[&["init", "--prefix", "x"], &["list"]]), None);
        // Every other scenario still gets the harness's own init first...
        assert_eq!(init(&[&["list"]]), pre_init);
        // ...including one whose init comes later: that init is a re-init.
        assert_eq!(init(&[&["list"], &["init", "--prefix", "x"]]), pre_init);
    }

    #[cfg(unix)]
    #[test]
    fn run_steps_runs_the_harness_init_only_when_the_scenario_does_not_own_it() {
        use std::os::unix::process::ExitStatusExt;
        // Record every argv run_scenario would execute; no process is spawned.
        let argvs = |steps: &[&[&str]]| {
            let mut seen: Vec<Vec<String>> = Vec::new();
            let trace = run_steps(&Scenario::new("t", "x", steps), &[], |args| {
                seen.push(args.to_vec());
                Ok(std::process::Output {
                    status: std::process::ExitStatus::from_raw(0),
                    stdout: Vec::new(),
                    stderr: Vec::new(),
                })
            })
            .unwrap();
            // The harness init is not part of the trace.
            assert_eq!(trace.steps.len(), steps.len());
            seen
        };
        let v = |a: &[&str]| a.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        let pre_init = v(&["init", "-p", "x", "--quiet"]);
        // A scenario that owns its init: only its own steps run.
        assert_eq!(
            argvs(&[&["init", "--prefix", "x"], &["list"]]),
            vec![v(&["init", "--prefix", "x"]), v(&["list"])]
        );
        // No init step: the harness init runs first.
        assert_eq!(argvs(&[&["list"]]), vec![pre_init.clone(), v(&["list"])]);
        // A later init is a re-init after the harness init.
        assert_eq!(
            argvs(&[&["list"], &["init", "--prefix", "x"]]),
            vec![pre_init, v(&["list"]), v(&["init", "--prefix", "x"])]
        );
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

    #[cfg(unix)]
    #[test]
    fn run_scenario_pins_the_tip_seed_for_every_step() {
        let _spawn = SPAWN.lock().unwrap_or_else(|e| e.into_inner());
        // printenv stands in for bd: each step prints BEADS_TIP_SEED from the
        // environment the harness hands every bd step (the harness's own
        // `init -p t --quiet` just exits non-zero, which run_scenario ignores).
        // Unset, real bd seeds its tip roll from the clock, so the same binary
        // prints a "💡 Tip:" line on some runs and not on others.
        let sc = Scenario::new("tip_seed", "t", &[&["BEADS_TIP_SEED"], &["BEADS_TIP_SEED"]]);
        let trace = run_scenario(Path::new("printenv"), &sc).expect("run printenv");
        assert_eq!(trace.steps.len(), 2);
        let want = format!("{TIP_SEED}\n");
        for step in &trace.steps {
            assert_eq!((step.exit, step.stdout.as_str()), (0, want.as_str()));
        }
    }
}
