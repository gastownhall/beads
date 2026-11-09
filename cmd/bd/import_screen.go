package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// importSource is where one parsed issue row came from: its 1-based physical
// line in the input and the line's bytes without the terminator. A rejected
// row is reported by that line and written out byte for byte from raw, never
// re-serialized.
type importSource struct {
	line int
	raw  []byte
}

// importRecords is one parsed JSONL stream: the issue rows, the memory
// records, and the source line of every issue row, keyed by the row's pointer
// so it survives the dedup and stale filters (which keep pointers).
type importRecords struct {
	issues   []*types.Issue
	memories []memoryRecord
	sources  map[*types.Issue]importSource
}

// scanImportRecords is the one JSONL reader every import path shares: `bd
// import` / `bd import -` (parseImportRecords) and the recovery paths
// (parseJSONLFile: bootstrap, init --from-jsonl, auto-import). Blank lines and
// the optional _schema header are skipped, memory records are collected,
// tombstones are skipped, and "wisp_plane" / the legacy "wisp" alias are
// honored via applyImportWispPlane. Every physical line is counted, so a line
// that is not JSON, or JSON that does not decode as a record, fails the whole
// read with an error naming that line: a truncated or conflicted file must not
// be imported minus its damaged part.
func scanImportRecords(r io.Reader) (*importRecords, error) {
	scanner := bufio.NewScanner(r)
	// Allow up to 64MB per line for large descriptions.
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	recs := &importRecords{sources: make(map[*types.Issue]importSource)}
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		// ScanLines strips the terminator, \r of a CRLF included.
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var peek map[string]json.RawMessage
		if err := json.Unmarshal(line, &peek); err != nil {
			return nil, fmt.Errorf("line %d: failed to parse JSONL line: %w", lineNum, err)
		}

		// Skip the optional beads-jsonl header record (§J1.3). A canonical
		// export may prepend a schema+provenance line, e.g.
		// {"_schema":"beads-jsonl/1","_dolt_branch":"main","_sort":"stable-v1"}.
		// It carries no _type and no issue fields; without this guard it falls
		// through to the issue path, unmarshals into an empty Issue, and is
		// refused with "title is required". Identified by the _schema
		// sentinel, which real issue/memory records never carry.
		if _, isHeader := peek["_schema"]; isHeader {
			continue
		}

		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal(line, &mem); err != nil {
					return nil, fmt.Errorf("line %d: failed to parse memory record: %w", lineNum, err)
				}
				if mem.Key != "" && mem.Value != "" {
					recs.memories = append(recs.memories, mem)
				}
				continue
			}
		}

		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			return nil, fmt.Errorf("line %d: failed to parse issue from JSONL: %w", lineNum, err)
		}
		// Skip tombstone entries: these are deleted issues exported by older
		// versions (pre-v0.50) with status "tombstone" and deleted_at set.
		// They are not valid for re-import since "tombstone" is not a real status.
		if issue.Status == "tombstone" {
			continue
		}
		applyImportWispPlane(peek, &issue)
		issue.SetDefaults()
		recs.issues = append(recs.issues, &issue)
		recs.sources[&issue] = importSource{line: lineNum, raw: bytes.Clone(line)}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("line %d: failed to scan JSONL: %w", lineNum+1, err)
	}
	return recs, nil
}

// importRejection is one issue row the import screen refused: its source
// line, its id (empty for a title-only row) and the writer's own validation
// message. raw is the line verbatim, for --rejects and the recovery
// quarantine.
type importRejection struct {
	Line   int    `json:"line"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
	raw    []byte
	issue  *types.Issue
}

// describe names the rejected row the way every report does:
// "line 2 (issue bd-abc)".
func (r importRejection) describe() string {
	return describeImportRow(r.Line, r.ID)
}

func describeImportRow(line int, id string) string {
	if id == "" {
		return fmt.Sprintf("line %d", line)
	}
	return fmt.Sprintf("line %d (issue %s)", line, id)
}

// importVocabularyReader reads the custom statuses and types the writer
// validates inserts against. It must return a resolver error rather than
// fall back: screening against a guessed vocabulary would reject rows the
// writer accepts.
type importVocabularyReader func(ctx context.Context) (customStatuses, customTypes []string, err error)

// storeImportVocabulary reads the vocabulary from a direct store through its
// issueops.InsertVocabularyReader capability, looking beneath the CLI's
// decorators (hooks, telemetry) when the outer layer does not offer it. A
// store with neither cannot screen a row that needs the vocabulary, and says
// so instead of guessing.
func storeImportVocabulary(st storage.DoltStorage) importVocabularyReader {
	return func(ctx context.Context) ([]string, []string, error) {
		reader, ok := st.(issueops.InsertVocabularyReader)
		if !ok && st != nil {
			reader, ok = storage.UnwrapStore(st).(issueops.InsertVocabularyReader)
		}
		if !ok {
			return nil, nil, fmt.Errorf("store %T cannot read the custom status/type vocabulary", st)
		}
		return reader.ReadInsertVocabulary(ctx)
	}
}

// proxiedImportVocabulary reads the vocabulary through the proxied-server unit
// of work's config use case: the same table/config/YAML composition its
// ImportBatch validates against, errors returned.
func proxiedImportVocabulary(ctx context.Context) ([]string, []string, error) {
	if uowProvider == nil {
		return nil, nil, errors.New("proxied-server UOW provider not initialized")
	}
	type vocabulary struct{ statuses, types []string }
	out, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (vocabulary, error) {
		statuses, err := uw.ConfigUseCase().GetCustomStatuses(ctx)
		if err != nil {
			return vocabulary{}, err
		}
		customTypes, err := uw.ConfigUseCase().GetCustomTypes(ctx)
		if err != nil {
			return vocabulary{}, err
		}
		return vocabulary{statuses: types.CustomStatusNames(statuses), types: customTypes}, nil
	})
	return out.statuses, out.types, err
}

// needsCustomVocabulary reports whether validating issue depends on the
// configured custom statuses or types: only a value outside the built-in sets
// does, so an import of built-in values never reads the vocabulary.
func needsCustomVocabulary(issue *types.Issue) bool {
	return !issue.Status.IsValid() || !issue.IssueType.IsBuiltIn()
}

// screenImportIssues validates every row an import is about to write, before
// it writes any of them, with the writer's own checks
// (issueops.ValidateIssueForInsert) and vocabulary. It returns the rows that
// pass, in order, and the rejected ones in source order. Rows are not
// modified. The vocabulary is read at most once, and only when a row uses a
// non-built-in status or type; a read failure stops the import (nothing has
// been written yet) and is reported against the row that needed it, which is
// not rejected. The writer still validates every row it writes: a vocabulary
// change between this read and the write is caught there.
func screenImportIssues(ctx context.Context, issues []*types.Issue, sources map[*types.Issue]importSource, readVocabulary importVocabularyReader) ([]*types.Issue, []importRejection, error) {
	var (
		vocabularyRead              bool
		customStatuses, customTypes []string
		rejected                    []importRejection
	)
	kept := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			kept = append(kept, issue)
			continue
		}
		src := sources[issue]
		if !vocabularyRead && needsCustomVocabulary(issue) {
			if readVocabulary == nil {
				return nil, nil, fmt.Errorf("%s: cannot validate it: no custom status/type vocabulary reader", describeImportRow(src.line, issue.ID))
			}
			statuses, typeNames, err := readVocabulary(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: cannot validate status %q / type %q: reading the configured custom statuses and types failed: %w", describeImportRow(src.line, issue.ID), issue.Status, issue.IssueType, err)
			}
			customStatuses, customTypes, vocabularyRead = statuses, typeNames, true
		}
		if err := issueops.ValidateIssueForInsert(issue, customStatuses, customTypes); err != nil {
			rejected = append(rejected, importRejection{Line: src.line, ID: issue.ID, Reason: err.Error(), raw: src.raw, issue: issue})
			continue
		}
		kept = append(kept, issue)
	}
	sort.SliceStable(rejected, func(i, j int) bool { return rejected[i].Line < rejected[j].Line })
	return kept, rejected, nil
}

// withoutRejectedRows returns issues minus the rejected rows (by pointer).
func withoutRejectedRows(issues []*types.Issue, rejected []importRejection) []*types.Issue {
	if len(rejected) == 0 {
		return issues
	}
	drop := make(map[*types.Issue]struct{}, len(rejected))
	for _, r := range rejected {
		drop[r.issue] = struct{}{}
	}
	kept := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if _, rejectedRow := drop[issue]; !rejectedRow {
			kept = append(kept, issue)
		}
	}
	return kept
}

// excludeRejectedFromChangePlan drops from a pre-filter plan the ids whose
// every row the screen rejected, so the report does not claim an update (or a
// timestamp tie) for a row that is not written. An id with a kept row keeps
// its entries.
func excludeRejectedFromChangePlan(plan *importChangePlan, kept []*types.Issue, rejected []importRejection) {
	if len(rejected) == 0 {
		return
	}
	keptIDs := make(map[string]struct{}, len(kept))
	for _, issue := range kept {
		if issue != nil {
			keptIDs[issue.ID] = struct{}{}
		}
	}
	gone := make(map[string]struct{})
	for _, r := range rejected {
		if _, stillWritten := keptIDs[r.ID]; !stillWritten {
			gone[r.ID] = struct{}{}
		}
	}
	isGone := func(id string) bool { _, ok := gone[id]; return ok }
	updates := plan.Updates[:0:0]
	for _, change := range plan.Updates {
		if !isGone(change.ID) {
			updates = append(updates, change)
		}
	}
	plan.Updates = updates
	ties := plan.TieKeptLocal[:0:0]
	for _, id := range plan.TieKeptLocal {
		if !isGone(id) {
			ties = append(ties, id)
		}
	}
	plan.TieKeptLocal = ties
}

// strictImportError is what `bd import` without --skip-invalid returns when
// the screen rejected a row. It names the first invalid line and the
// validator's reason, says that nothing was imported (true: the screen runs
// before any write, so a user who believed some rows landed would otherwise
// repair the file and apply the good rows twice), and names the way past it.
func strictImportError(rejected []importRejection) error {
	first := rejected[0]
	var b strings.Builder
	fmt.Fprintf(&b, "invalid record on %s: %s", first.describe(), first.Reason) //nolint:gosec // G705: error text, not a browser context
	if more := len(rejected) - 1; more > 0 {
		fmt.Fprintf(&b, " (and %d more invalid record(s))", more) //nolint:gosec // G705: error text, not a browser context
	}
	b.WriteString("\nnothing was imported. To import the valid records and set the invalid ones aside,\n")
	b.WriteString("re-run with --skip-invalid; add --rejects <file> to keep the skipped lines for repair.")
	return errors.New(b.String())
}

// importRejectsPath is `bd import --rejects`: where the skipped lines go.
// importSkipInvalid is --skip-invalid.
var (
	importSkipInvalid  bool
	importRejectsPath  string
	importRejectsShown = 20
)

// validateImportRejectsFlag refuses a --rejects the import cannot honor safely:
// without --skip-invalid (an output flag must not silently change which rows
// are imported), or naming the input itself, through any alias — the same
// cleaned path, a symlink to it, or a hard link — since writing it would
// destroy the source being read.
func validateImportRejectsFlag(source string, fromStdin bool) error {
	if importRejectsPath == "" {
		return nil
	}
	if !importSkipInvalid {
		return errors.New("--rejects requires --skip-invalid (it writes the lines --skip-invalid sets aside)")
	}
	if fromStdin {
		return nil
	}
	if sameImportFile(source, importRejectsPath) {
		return fmt.Errorf("--rejects %s is the input file; choose a different path", importRejectsPath)
	}
	return nil
}

// sameImportFile reports whether two paths name the same file: equal once
// made absolute with symlinks resolved, or the same inode (os.SameFile).
func sameImportFile(a, b string) bool {
	canonical := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return filepath.Clean(p)
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return resolved
		}
		// A path that does not exist yet: resolve its directory.
		if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			return filepath.Join(dir, filepath.Base(abs))
		}
		return abs
	}
	if canonical(a) == canonical(b) {
		return true
	}
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

// writeImportRejects atomically replaces path with the rejected lines, verbatim
// and in source order, each terminated by LF. An empty list writes an empty
// file, so a reused path never holds a previous run's rejects. The file is
// written to a temporary sibling and renamed into place, so a failure leaves
// the previous file (or none) rather than a partial one.
func writeImportRejects(path string, rejected []importRejection) error {
	var buf bytes.Buffer
	for _, r := range rejected {
		buf.Write(r.raw)
		buf.WriteByte('\n')
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write rejects file %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write rejects file %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write rejects file %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("write rejects file %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("write rejects file %s: %w", path, err)
	}
	return nil
}

// screenImportWriteSet applies the `bd import` screening policy to the rows an
// import is about to write: rejects fail the import unless --skip-invalid, and
// --rejects is written (outside a dry run) before the caller's first database
// write, so a rejects file that cannot be written stops the import before it
// changes anything. The rejects file is not a commit receipt: a later write
// failure leaves it in place.
func screenImportWriteSet(ctx context.Context, writeSet []*types.Issue, sources map[*types.Issue]importSource, readVocabulary importVocabularyReader, dryRun bool) ([]*types.Issue, []importRejection, string, error) {
	kept, rejected, err := screenImportIssues(ctx, writeSet, sources, readVocabulary)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w\nnothing was imported", err)
	}
	if len(rejected) > 0 && !importSkipInvalid {
		return nil, nil, "", strictImportError(rejected)
	}
	if importRejectsPath == "" || dryRun {
		return kept, rejected, "", nil
	}
	if err := writeImportRejects(importRejectsPath, rejected); err != nil {
		return nil, nil, "", fmt.Errorf("%w\nnothing was imported", err)
	}
	return kept, rejected, importRejectsPath, nil
}

// renderImportRejections lists the rejected rows on stderr: the first
// importRejectsShown of them, then the total.
func renderImportRejections(w io.Writer, rejected []importRejection, rejectsFile string, dryRun bool) {
	if len(rejected) == 0 {
		return
	}
	verb := "Skipped"
	if dryRun {
		verb = "Would skip"
	}
	fmt.Fprintf(w, "%s %d invalid record(s):\n", verb, len(rejected))
	for i, r := range rejected {
		if i == importRejectsShown {
			fmt.Fprintf(w, "  ... and %d more\n", len(rejected)-importRejectsShown)
			break
		}
		fmt.Fprintf(w, "  %s: %s\n", r.describe(), r.Reason) //nolint:gosec // G705: stderr, not a browser context
	}
	if rejectsFile != "" {
		fmt.Fprintf(w, "Skipped lines saved to %s\n", rejectsFile) //nolint:gosec // G705: stderr, not a browser context
	}
}

// recoveryQuarantinePath is where a recovery import (bootstrap, init
// --from-jsonl, auto-import) sets aside the lines it skipped: beside the
// source, which it never modifies.
func recoveryQuarantinePath(source string) string {
	return source + ".rejected.jsonl"
}

// quarantineRecoveryRejects saves a recovery import's rejected lines to
// <source>.rejected.jsonl (replacing an earlier quarantine) and warns on
// stderr with the count, a representative line and reason, and where they
// went. With nothing rejected it does nothing, leaving any earlier quarantine
// alone. A quarantine that cannot be written is a warning, not a failure: the
// source still holds every line, and the valid rows are still restored. It
// returns the path written, or "" when nothing was saved.
func quarantineRecoveryRejects(w io.Writer, source string, rejected []importRejection) string {
	if len(rejected) == 0 {
		return ""
	}
	qpath := recoveryQuarantinePath(source)
	fmt.Fprintf(w, "warning: skipped %d invalid record(s) from %s; first: %s: %s\n", len(rejected), source, rejected[0].describe(), rejected[0].Reason) //nolint:gosec // G705: stderr, not a browser context
	if err := writeImportRejects(qpath, rejected); err != nil {
		fmt.Fprintf(w, "warning: could not save the skipped lines to %s: %v; they are still in %s\n", qpath, err, source) //nolint:gosec // G705: stderr, not a browser context
		return ""
	}
	fmt.Fprintf(w, "  the skipped lines were saved to %s; repair them (or register the custom status/type they use) and re-import with: bd import %s\n", qpath, qpath) //nolint:gosec // G705: stderr, not a browser context
	return qpath
}
