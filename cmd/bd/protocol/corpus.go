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
}

// Fixed issue IDs used by the PLAN. Pinning IDs removes the largest source
// of nondeterminism (generated hash IDs) so the only thing left to
// canonicalize is timestamps.
const (
	CorpusRootID    = "corpus-root"
	CorpusDepID     = "corpus-dep"
	CorpusClosedID  = "corpus-closed"  // created, then closed + reopened
	CorpusDeletedID = "corpus-deleted" // created, then deleted
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
	return []Capture{
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
		// update pins label + metadata coercion (phase=2 -> integer); close pins
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

// CanonicalizeJSON normalizes a bd JSON blob so that two independent runs
// produce byte-identical output:
//
//  1. Any string value that is an RFC3339/RFC3339Nano timestamp is replaced
//     with "<TS>".
//  2. Any array whose elements are all objects is stably sorted by the
//     element's "id" field (falling back to the element's canonical-JSON
//     form when "id" is absent or equal), so list/ready/sql ordering is
//     deterministic regardless of storage iteration order.
//  3. The result is re-marshaled with 2-space indentation and sorted keys
//     (Go's encoding/json sorts map keys), yielding a minimal, byte-stable
//     diff.
//
// The input need not be a single object; arrays and scalars are handled.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
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

	canon := canonValue(v)

	out, err := marshalCanonical(canon)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: marshal: %w", err)
	}
	return out, nil
}

// canonValue recursively normalizes a decoded JSON value in place.
func canonValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
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
			t[k] = canonValue(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = canonValue(child)
		}
		sortObjectArray(t)
		return t
	case string:
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
