// corpus.go — producer-side logic for the Beads↔consumer cross-version
// contract-test system (Phase 2).
//
// This file is the dependency-free, unit-testable core: it defines the
// deterministic command PLAN that exercises bd's stable CLI/JSON surface,
// a canonicalizer that strips nondeterminism (timestamps, ordering) so two
// independent runs produce byte-identical output, and the manifest that
// records the corpus's provenance.
//
// A downstream consumer vendors the generated corpus (testdata/corpus/) and replays it
// against its own consumer to detect cross-version drift without needing a
// live bd. Everything here must stay free of test-only and bd-internal
// imports so it can be reasoned about (and reused) in isolation.
package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Capture is a single named command in the corpus PLAN. Name is the stable
// blob filename (without extension); Args is the exact bd argument vector
// (excluding the bd binary itself and the global --json flag, which the
// runner appends where appropriate).
type Capture struct {
	Name string
	Args []string
	// Kind says how the generator records this step. The zero value,
	// CaptureJSON, is every capture that predates the native-fix section.
	Kind CaptureKind
	// Write marks a capture that mutates the store. The generator records how
	// many Dolt commits it added in testdata/corpus_commits.golden, which is not
	// part of the shipped corpus.
	Write bool
	// Mints marks a capture whose JSON output is an issue whose id bd minted
	// (a hash id, not pinned with --id). The generator remembers that id under
	// MintedPlaceholder(Name): a later capture names it in Args with the
	// placeholder, and every blob reports it as the placeholder.
	Mints bool
}

// CaptureKind is how the generator turns one PLAN step's stdout into a blob.
type CaptureKind int

const (
	// CaptureJSON: stdout is exactly one JSON document, canonicalized.
	CaptureJSON CaptureKind = iota
	// CaptureEvents: stdout is `bd history <id> --events --json`. Each event
	// row carries a random id, and old_value/new_value embed issue snapshots
	// whose timestamps are text inside a string; the blob drops the row id
	// and replaces each embedded timestamp with <TS> (see Projection.Events).
	CaptureEvents
	// CaptureText: stdout is not JSON (bd q prints the bare new id even under
	// --json). The blob is that stdout as a single JSON string.
	CaptureText
	// CaptureSetup prepares state for later captures: it must exit 0 and
	// produces no blob, because its command's shape is pinned elsewhere.
	CaptureSetup
)

// MintedPlaceholder is the placeholder a blob reports for the id the Mints
// capture named name minted, and the token a later capture's Args use to name
// that issue.
func MintedPlaceholder(name string) string { return "<MINTED:" + name + ">" }

// Fixed issue IDs used by the PLAN. Pinning IDs removes the largest source
// of nondeterminism (generated hash IDs) so the only thing left to
// canonicalize is timestamps.
const (
	CorpusRootID    = "corpus-root"
	CorpusDepID     = "corpus-dep"
	CorpusClosedID  = "corpus-closed"  // created, then closed + reopened
	CorpusDeletedID = "corpus-deleted" // created, then deleted
)

// Pinned ids for the native-fix section (G4a). Each write gets a subject of its
// own, so its events blob holds the subject's create plus that write and no
// other capture's history.
const (
	CorpusAnchorID     = "corpus-anchor" // the rich subjects' shared non-blocking edge target
	CorpusPriorityID   = "corpus-priority"
	CorpusTagID        = "corpus-tag"
	CorpusNoteID       = "corpus-note"
	CorpusParentID     = "corpus-parent"
	CorpusQuickChildID = "corpus-parent.1" // the hierarchical id bd q --parent reserves
	CorpusCanonicalID  = "corpus-canonical"
	CorpusDuplicateID  = "corpus-duplicate"
	CorpusSupersededID = "corpus-superseded"
	CorpusRelFromID    = "corpus-relfrom"
	CorpusRelToID      = "corpus-relto"
	CorpusDeferredID   = "corpus-deferred"
	CorpusCloserID     = "corpus-closer"
	CorpusBlockerID    = "corpus-blocker"
	CorpusBlockedID    = "corpus-blocked"
	CorpusUnblockedID  = "corpus-unblocked"
)

// errorCaptureName is the single PLAN capture that deliberately targets a
// missing issue, so it is the one capture expected to exit non-zero. It pins the
// exit-codes-and-errors half of the contract — a non-zero exit plus the
// {error, schema_version} stdout envelope (see CATALOG.md) — while every other
// capture must exit 0. The generator special-cases this name in both the
// exit-status check and the error-envelope guard.
const errorCaptureName = "error"

// CorpusPlan returns the ordered, deterministic list of captures. The order
// matters: create steps must run before the read steps that observe them,
// and the dependency must be added before dep_list is captured.
//
// Each Capture's Args are passed verbatim to bd. Read commands include
// --json so the output is the structured contract surface; the "error"
// capture deliberately targets a missing issue to pin the error envelope.
func CorpusPlan() []Capture {
	return append([]Capture{
		// CREATE: --force lets us pin custom IDs despite bd's per-database random
		// ID prefix; pinned IDs make every downstream read deterministic. Four
		// beads: root + dep exercise reads/dependencies; closed + deleted are
		// created here so the close/reopen and delete mutations below have stable
		// subjects without disturbing root/dep.
		{
			Name: "create_root",
			Args: []string{"create", "Corpus root issue", "--id", CorpusRootID, "--force", "--priority", "1", "--type", "feature", "--description", "deterministic corpus root", "--json"},
		},
		{
			Name: "create_dep",
			Args: []string{"create", "Corpus dependency issue", "--id", CorpusDepID, "--force", "--priority", "2", "--type", "task", "--description", "deterministic corpus dependency", "--json"},
		},
		{
			Name: "create_closed",
			Args: []string{"create", "Corpus closeable issue", "--id", CorpusClosedID, "--force", "--priority", "3", "--type", "task", "--description", "deterministic corpus closeable", "--json"},
		},
		{
			Name: "create_deleted",
			Args: []string{"create", "Corpus deletable issue", "--id", CorpusDeletedID, "--force", "--priority", "3", "--type", "task", "--description", "deterministic corpus deletable", "--json"},
		},
		// dep_add pins the dual-key dependency-edge shape {issue_id, depends_on_id,
		// type, status}.
		{
			Name: "dep_add",
			Args: []string{"dep", "add", CorpusRootID, CorpusDepID, "--type", "blocks", "--json"},
		},
		// READS captured before the mutations below, so show/dep_list pin the
		// pre-mutation root (the stable, long-standing blobs).
		{
			Name: "show",
			Args: []string{"show", CorpusRootID, "--json"},
		},
		{
			Name: "dep_list",
			Args: []string{"dep", "list", CorpusRootID, "--json"},
		},
		// MUTATIONS: update/close/reopen return an array of the affected issue;
		// update pins label + metadata coercion (phase=2 -> string "2", since
		// --set-metadata always stores values as JSON strings); close pins
		// close_reason + closed_at; dep_remove/delete pin their confirmation shapes.
		{
			Name: "update",
			Args: []string{"update", CorpusRootID, "--json", "--priority", "0", "--add-label", "corpus-label", "--set-metadata", "phase=2", "--description", "updated corpus root"},
		},
		{
			Name: "close",
			Args: []string{"close", "--force", "--json", "--reason", "corpus close reason exceeding twenty chars", CorpusClosedID},
		},
		{
			Name: "reopen",
			Args: []string{"reopen", "--json", CorpusClosedID},
		},
		{
			Name: "dep_remove",
			Args: []string{"dep", "remove", CorpusRootID, CorpusDepID, "--json"},
		},
		{
			Name: "delete",
			Args: []string{"delete", "--force", "--json", CorpusDeletedID},
		},
		// READS captured after the mutations, so list/ready/count reflect the
		// full post-mutation state (updated root, reopened bead, removed edge,
		// deleted bead gone).
		{
			Name: "list",
			Args: []string{"list", "--all", "--json"},
		},
		{
			Name: "ready",
			Args: []string{"ready", "--json"},
		},
		{
			Name: "count",
			Args: []string{"count", "--json"},
		},
		{
			Name: "version",
			Args: []string{"version", "--json"},
		},
		// The error capture pins the {error, schema_version} envelope bd emits on
		// stdout for a missing issue — a downstream consumer's isBdNotFound classifier depends on
		// it — plus the non-zero exit status bd returns (see checkCaptureExit).
		{
			Name: errorCaptureName,
			Args: []string{"show", "nonexistent-xyz-corpus", "--json"},
		},
	}, nativeFixCaptures()...)
}

// nativeSeed is a CaptureSetup step creating one pinned-id subject.
func nativeSeed(id, title string, extra ...string) Capture {
	args := append([]string{"create", title, "--id", id, "--force", "--type", "task", "--priority", "3"}, extra...)
	return Capture{Name: "seed_" + strings.ReplaceAll(strings.TrimPrefix(id, "corpus-"), ".", "_"), Args: append(args, "--json"), Kind: CaptureSetup}
}

// richSeed is a subject carrying every field a role's result can drop or add
// relative to a fresh read: a description, notes, a label, metadata and one
// non-blocking edge. (No assignee: bd close refuses an issue assigned to
// another actor.) A plain subject hides the difference
// (verified: fixes-b B8 changes the defer blob only when the subject has an edge).
func richSeed(id, title string) Capture {
	return nativeSeed(id, title,
		"--description", "deterministic "+id,
		"--notes", id+" notes",
		"--labels", "corpus-native",
		"--metadata", `{"corpus":"native"}`,
		"--deps", "discovered-from:"+CorpusAnchorID,
	)
}

// eventsOf is the events capture for one subject.
func eventsOf(name, id string) Capture {
	return Capture{Name: name, Args: []string{"history", id, "--events", "--json"}, Kind: CaptureEvents}
}

// nativeFixCaptures is the native-fix section (G4a): the direct-mode commands
// the fork's native fixes B1-B8 move onto roles, plus the reads B9/B10 and
// D3-D6 touch, each pinned as today's behavior. It is appended after the
// legacy captures so none of its state reaches their blobs. Every write is
// followed by an events capture of its subject and gets a commit-ledger line.
func nativeFixCaptures() []Capture {
	return []Capture{
		nativeSeed(CorpusAnchorID, "Corpus anchor subject"),
		richSeed(CorpusPriorityID, "Corpus priority subject"),
		richSeed(CorpusTagID, "Corpus tag subject"),
		richSeed(CorpusNoteID, "Corpus note subject"),
		richSeed(CorpusParentID, "Corpus parent subject"),
		nativeSeed(CorpusCanonicalID, "Corpus canonical subject"),
		richSeed(CorpusDuplicateID, "Corpus duplicate subject"),
		richSeed(CorpusSupersededID, "Corpus superseded subject"),
		richSeed(CorpusRelFromID, "Corpus relate-from subject"),
		richSeed(CorpusRelToID, "Corpus relate-to subject"),
		richSeed(CorpusDeferredID, "Corpus deferred subject"),
		richSeed(CorpusCloserID, "Corpus closer subject"),

		{Name: "priority", Args: []string{"priority", CorpusPriorityID, "1", "--json"}, Write: true},
		eventsOf("priority_events", CorpusPriorityID),
		{Name: "tag", Args: []string{"tag", CorpusTagID, "corpus-tag-label", "--json"}, Write: true},
		eventsOf("tag_events", CorpusTagID),
		{Name: "note", Args: []string{"note", CorpusNoteID, "corpus note text", "--json"}, Write: true},
		eventsOf("note_events", CorpusNoteID),
		{Name: "q_parent", Args: []string{"q", "Corpus quick child", "--parent", CorpusParentID, "--json"}, Kind: CaptureText, Write: true},
		{Name: "q_parent_show", Args: []string{"show", CorpusQuickChildID, "--json"}},
		eventsOf("q_parent_events", CorpusQuickChildID),
		{Name: "todo_add", Args: []string{"todo", "add", "Corpus todo item", "--json"}, Write: true, Mints: true},
		eventsOf("todo_add_events", MintedPlaceholder("todo_add")),
		{Name: "duplicate", Args: []string{"duplicate", CorpusDuplicateID, "--of", CorpusCanonicalID, "--json"}, Write: true},
		eventsOf("duplicate_events", CorpusDuplicateID),
		{Name: "supersede", Args: []string{"supersede", CorpusSupersededID, "--with", CorpusCanonicalID, "--json"}, Write: true},
		eventsOf("supersede_events", CorpusSupersededID),
		{Name: "dep_relate", Args: []string{"dep", "relate", CorpusRelFromID, CorpusRelToID, "--json"}, Write: true},
		eventsOf("dep_relate_events", CorpusRelFromID),
		{Name: "dep_unrelate", Args: []string{"dep", "unrelate", CorpusRelFromID, CorpusRelToID, "--json"}, Write: true},
		eventsOf("dep_unrelate_events", CorpusRelFromID),
		{Name: "defer", Args: []string{"defer", CorpusDeferredID, "--reason", "corpus defer reason", "--until", "2099-01-01", "--json"}, Write: true},
		eventsOf("defer_events", CorpusDeferredID),
		{Name: "undefer", Args: []string{"undefer", CorpusDeferredID, "--json"}, Write: true},
		eventsOf("undefer_events", CorpusDeferredID),
		{Name: "close_claim_next", Args: []string{"close", CorpusCloserID, "--claim-next", "--json"}, Write: true},
		eventsOf("close_claim_next_events", CorpusCloserID),
		{Name: "unclaim", Args: []string{"unclaim", CorpusRootID, "--json"}, Write: true},
		eventsOf("unclaim_events", CorpusRootID),

		nativeSeed(CorpusBlockerID, "Corpus blocker subject"),
		nativeSeed(CorpusBlockedID, "Corpus blocked subject", "--deps", CorpusBlockerID),
		nativeSeed(CorpusUnblockedID, "Corpus unblocked subject", "--deps", CorpusCloserID),
		{Name: "ready_explain", Args: []string{"ready", "--explain", "--json"}},
		{Name: "info", Args: []string{"info", "--json"}},
		{Name: "show_refs", Args: []string{"show", CorpusCanonicalID, "--refs", "--json"}},
		{Name: "show_children", Args: []string{"show", CorpusParentID, "--children", "--json"}},
	}
}

// timestampRE matches RFC3339 / RFC3339Nano timestamps anywhere in a string
// value. We anchor it to the full string so we only replace values that are
// timestamps in their entirety, not strings that happen to embed a date.
var timestampRE = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$`,
)

// tsPlaceholder is the canonical stand-in for any timestamp value.
const tsPlaceholder = "<TS>"

// provenanceDropKeys are removed entirely during canonicalization: they vary in
// both value AND presence across builds. `bd version --json` embeds "commit"
// from Go's VCS stamping, which a local worktree build may omit while a CI
// clean-clone build includes — so it must be dropped, not just normalized.
var provenanceDropKeys = map[string]bool{"commit": true}

// provenanceValueKeys have a stable presence but a release/build-specific value
// (the bd version number, git branch, and build tag from `bd version --json`).
// The corpus pins the SHAPE of the version command — that these fields exist and
// schema_version is present — not the release identity, which changes every time
// beads bumps its version. Their values are replaced with a placeholder so the
// corpus stays reproducible across versions; gc's actual version parsing/gating
// is covered by the cross-version matrix and bd_version_pin_test, not here.
var provenanceValueKeys = map[string]string{
	"version": "<VERSION>",
	"branch":  "<BRANCH>",
	"build":   "<BUILD>",
}

// revisionPlaceholder is the canonical stand-in for a guarded-write revision
// (row_lock) token value.
const revisionPlaceholder = "<REVISION>"

// volatileValueKeys have a stable presence but a per-write-random value, so the
// corpus pins that the field EXISTS (its wire shape) while replacing its value
// with a placeholder — exactly as it does for timestamps. `bd show --json`
// exposes the guarded-write revision (the row_lock optimistic-concurrency
// token), which the engine rewrites to a fresh random value on every
// lifecycle/ownership write; pinning its literal value would break both the
// golden byte-comparison and the double-run determinism check. Same bare-key-
// name scope caveat as the provenance transforms below: `revision` is currently
// emitted only by `bd show --json`. If a future command ever emits a
// deterministic field named `revision`, scope this to the show capture (thread
// Capture.Name through generation) before relying on it.
var volatileValueKeys = map[string]string{
	"revision": revisionPlaceholder,
}

// CanonicalizeJSON normalizes a bd JSON blob so that two independent runs
// produce byte-identical output:
//
//  1. Any string value that is an RFC3339/RFC3339Nano timestamp is replaced
//     with "<TS>".
//  2. Any per-write-random token value (the guarded-write "revision" /
//     row_lock, matched by key name) is replaced with "<REVISION>", pinning
//     the field's presence without its nondeterministic value.
//  3. Any array whose elements are all objects is stably sorted by the
//     element's "id" field (falling back to the element's canonical-JSON
//     form when "id" is absent or equal), so list/ready/sql ordering is
//     deterministic regardless of storage iteration order.
//  4. The result is re-marshaled with 2-space indentation and sorted keys
//     (Go's encoding/json sorts map keys), yielding a minimal, byte-stable
//     diff.
//
// The input need not be a single object; arrays and scalars are handled.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	return CanonicalizeJSONWith(raw, Projection{})
}

// embeddedTimestampRE matches an RFC3339 timestamp anywhere inside a string.
// Only CaptureEvents blobs use it: their old_value/new_value carry whole issue
// snapshots as JSON text, so the timestamps are substrings, not values.
var embeddedTimestampRE = regexp.MustCompile(
	`\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})`,
)

// Projection is the normalization a capture needs beyond CanonicalizeJSON's.
// The zero Projection is CanonicalizeJSON.
type Projection struct {
	// Literals maps a run-specific literal (the workspace's random id prefix
	// and directory, an id bd minted) to its placeholder. Every occurrence in
	// any string value or object key is replaced, longest literal first, before
	// object arrays are sorted, so the sort cannot see the random value.
	Literals map[string]string
	// Events drops "id" from every object carrying "event_type" (an audit
	// event row's random id) and replaces RFC3339 timestamps embedded inside
	// string values with <TS>.
	Events bool
}

// CanonicalizeJSONWith is CanonicalizeJSON plus p.
func CanonicalizeJSONWith(raw []byte, p Projection) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonicalize: decode: %w", err)
	}
	// A corpus capture must be exactly one JSON document. bd could print a
	// warning line (or, on a bug, a second JSON value) after the payload; that
	// trailing stdout would otherwise be silently dropped here and canonicalized
	// as if the command emitted clean JSON, while a real consumer decoding the
	// whole stream would choke on it. Require the stream to end after the first
	// value; only trailing whitespace, which the decoder skips, is allowed.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("canonicalize: unexpected second JSON value after the first")
		}
		return nil, fmt.Errorf("canonicalize: unexpected trailing data after JSON value: %w", err)
	}

	canon := canonValue(v, newProjector(p))

	out, err := marshalCanonical(canon)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: marshal: %w", err)
	}
	return out, nil
}

// projector applies a Projection to strings.
type projector struct {
	literals []string // longest first
	repl     map[string]string
	events   bool
}

func newProjector(p Projection) projector {
	lits := make([]string, 0, len(p.Literals))
	for k := range p.Literals {
		if k != "" {
			lits = append(lits, k)
		}
	}
	sort.Slice(lits, func(i, j int) bool {
		if len(lits[i]) != len(lits[j]) {
			return len(lits[i]) > len(lits[j])
		}
		return lits[i] < lits[j]
	})
	return projector{literals: lits, repl: p.Literals, events: p.Events}
}

func (pr projector) project(s string) string {
	for _, lit := range pr.literals {
		s = strings.ReplaceAll(s, lit, pr.repl[lit])
	}
	if pr.events {
		s = embeddedTimestampRE.ReplaceAllString(s, tsPlaceholder)
	}
	return s
}

// canonValue recursively normalizes a decoded JSON value in place.
func canonValue(v any, pr projector) any {
	switch t := v.(type) {
	case map[string]any:
		if pr.events {
			if _, isEvent := t["event_type"]; isEvent {
				delete(t, "id")
			}
		}
		if len(pr.literals) > 0 {
			// Collect the renames first: inserting into a map while ranging
			// over it may or may not visit the new key.
			type rename struct{ from, to string }
			var renames []rename
			for k := range t {
				if pk := pr.project(k); pk != k {
					renames = append(renames, rename{k, pk})
				}
			}
			for _, r := range renames {
				child := t[r.from]
				delete(t, r.from)
				t[r.to] = child
			}
		}
		for k, child := range t {
			// NOTE ON SCOPE: the provenance transforms below match by bare key
			// name at every nesting depth of every blob, not just the version
			// capture. That is safe only because these keys (commit/version/
			// branch/build) currently appear solely in the version blob
			// (`grep -rl '"version"' testdata/corpus` lists version blobs only).
			// If a future command ever emits a field named commit/version/branch/
			// build, this would silently drop or placeholder it and the corpus
			// would stop guarding that command's wire shape. Before adding such a
			// field, scope these transforms to the version capture (thread the
			// Capture.Name through generation) — note it cannot be done by "top
			// level only" because envelope mode nests these under "data".
			//
			// Drop build-provenance keys entirely so the canonical corpus does
			// not depend on how (or where) bd was built.
			if provenanceDropKeys[k] {
				delete(t, k)
				continue
			}
			// Replace release/build-specific values (version, branch, build) with
			// a placeholder: pin the version command's shape, not its identity.
			if ph, ok := provenanceValueKeys[k]; ok {
				if _, isStr := child.(string); isStr {
					t[k] = ph
					continue
				}
			}
			// Replace per-write-random token values (the guarded-write revision /
			// row_lock) with a placeholder: pin that bd show exposes a revision
			// field, not its nondeterministic value. Unlike the provenance keys
			// above this is unconditional — the value is replaced whatever its
			// JSON type, so the placeholder held when the token was a number and
			// still holds now that it is a decimal string — but it stays a
			// bare-key match, so keep the scope caveat in mind (see
			// volatileValueKeys).
			if ph, ok := volatileValueKeys[k]; ok {
				t[k] = ph
				continue
			}
			t[k] = canonValue(child, pr)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = canonValue(child, pr)
		}
		sortObjectArray(t)
		return t
	case string:
		t = pr.project(t)
		if timestampRE.MatchString(t) {
			return tsPlaceholder
		}
		return t
	default:
		// json.Number, bool, nil — already deterministic.
		return v
	}
}

// sortObjectArray stably sorts arr in place iff every element is an object.
// Elements are ordered by their "id" string when present; ties (or missing
// ids) fall back to the element's canonical-JSON encoding so the order is
// fully determined by content.
func sortObjectArray(arr []any) {
	for _, e := range arr {
		if _, ok := e.(map[string]any); !ok {
			return // mixed or scalar array — preserve original order
		}
	}
	sort.SliceStable(arr, func(i, j int) bool {
		return elemSortKey(arr[i]) < elemSortKey(arr[j])
	})
}

// elemSortKey derives a stable ordering key for an object array element:
// its "id" field plus its canonical-JSON form as a tiebreaker. Including the
// canonical form guarantees a total order even when ids collide or are
// absent.
func elemSortKey(e any) string {
	idPart := ""
	if m, ok := e.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			idPart = id
		}
	}
	body, err := marshalCanonical(e)
	if err != nil {
		return idPart
	}
	return idPart + "\x00" + string(body)
}

// marshalCanonical encodes v with sorted keys (json default) and 2-space
// indentation, without HTML-escaping, so the bytes are stable and minimal.
func marshalCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; keep it for clean diffs.
	return buf.Bytes(), nil
}

// BlobMeta records a single corpus blob's provenance: the bd command that
// produced it and the SHA-256 of its canonicalized bytes.
type BlobMeta struct {
	Cmd    string `json:"cmd"`
	SHA256 string `json:"sha256"`
}

// Manifest is the corpus index. It pins the schema version (the coordination
// canary), the generator identity, and a per-blob SHA-256 checksum so consumers
// can detect tampering or partial vendoring. It deliberately does not
// pin the live bd version/commit — see the field comment below for why the
// corpus stays version-agnostic.
type Manifest struct {
	SchemaVersion int `json:"schema_version"`
	// bd_version and bd_commit are intentionally omitted: both vary by build
	// environment / release and would make the committed manifest
	// non-reproducible. The corpus is version-agnostic (it pins wire SHAPES, not
	// a release identity); schema_version is the coordination canary and the
	// per-blob checksums are the integrity anchor. The generating bd version/
	// commit lives in the PR that updates the corpus.
	GeneratedBy   string              `json:"generated_by"`
	Canonicalized bool                `json:"canonicalized"`
	Blobs         map[string]BlobMeta `json:"blobs"`
}

// NewManifest builds a Manifest from the captured (mode → name → bytes)
// corpus. The blob key is "<mode>/<name>" (e.g. "flat/show",
// "envelope/show") and cmd is the joined bd argument vector for that
// capture. bytes are the already-canonicalized blob contents; the SHA is
// computed over them so the manifest validates exactly what's on disk.
func NewManifest(schemaVersion int, generatedBy string, plan []Capture, blobs map[string]map[string][]byte) Manifest {
	cmdByName := make(map[string]string, len(plan))
	for _, c := range plan {
		cmdByName[c.Name] = "bd " + joinArgs(c.Args)
	}

	out := make(map[string]BlobMeta)
	for mode, byName := range blobs {
		for name, data := range byName {
			sum := sha256.Sum256(data)
			out[mode+"/"+name] = BlobMeta{
				Cmd:    cmdByName[name],
				SHA256: hex.EncodeToString(sum[:]),
			}
		}
	}

	return Manifest{
		SchemaVersion: schemaVersion,
		GeneratedBy:   generatedBy,
		Canonicalized: true,
		Blobs:         out,
	}
}

// MarshalManifest encodes a Manifest deterministically (sorted keys, 2-space
// indent) so committing it produces stable diffs.
func MarshalManifest(m Manifest) ([]byte, error) {
	return marshalCanonical(m)
}

// joinArgs renders an argument vector as a single shell-ish string for the
// manifest's cmd field. Args containing spaces are quoted so multi-word titles
// and descriptions read correctly.
func joinArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsRune(a, ' ') {
			parts[i] = `"` + a + `"`
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}
