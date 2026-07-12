//! Differential conformance harness for `bd`.
//!
//! Runs the same curated CLI scenarios against two separately-built `bd`
//! binaries — a reference (the "before") and a candidate (the "after") — in
//! isolated workspaces, and diffs each step's observable behavior
//! (stdout/stderr/exit) with JSON-aware, array-order-significant comparison.
//! See `differential` for the runner and diff, and `scenarios` for the corpus.

pub mod differential;
pub mod scenarios;
