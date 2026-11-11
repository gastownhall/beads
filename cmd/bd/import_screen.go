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
	"runtime"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
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
// of work's config use case: the table/config/YAML composition its
// ImportBatch validates against (issueops.ComposeCustomTypes), errors
// returned. It fails closed where the writer's resolver
// (issueops.ResolveCustomTypesInTx) reads on: a custom_types query that fails
// for a reason other than a missing table, a row that does not scan, or a
// types.custom read that fails is an error here, so the import stops before
// writing anything, while ImportBatch would have fallen back and validated
// the rows.
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
// destroy the source being read. stdin is non-nil when the import reads it
// (`bd import -`; source is then unused): a pipe has no file a path can name,
// but `bd import - < file` reads a regular file, which is the input too.
func validateImportRejectsFlag(source string, stdin *os.File) error {
	if importRejectsPath == "" {
		return nil
	}
	if !importSkipInvalid {
		return errors.New("--rejects requires --skip-invalid (it writes the lines --skip-invalid sets aside)")
	}
	var collides bool
	if stdin != nil {
		collides = stdinReadsFile(stdin, importRejectsPath)
	} else {
		collides = sameImportFile(source, importRejectsPath)
	}
	if collides {
		return fmt.Errorf("--rejects %s is the input file; choose a different path", importRejectsPath)
	}
	return nil
}

// stdinReadsFile reports whether stdin is the regular file at path: the same
// file (os.SameFile), so a symlink or a hard link to it counts as well.
func stdinReadsFile(stdin *os.File, path string) bool {
	in, err := stdin.Stat()
	if err != nil || !in.Mode().IsRegular() {
		return false
	}
	out, err := os.Stat(path)
	return err == nil && os.SameFile(in, out)
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

// renderImportRejections lists the rows an import's screen rejected on
// stderr: the first importRejectsShown of them, then the total, where
// --rejects saved them, and how many dependencies on them the import skipped
// as well.
func renderImportRejections(w io.Writer, result importResultJSON) {
	rejected := result.Rejected
	if len(rejected) == 0 {
		return
	}
	verb := "Skipped"
	if result.DryRun {
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
	if result.RejectsFile != "" {
		fmt.Fprintf(w, "Skipped lines saved to %s\n", result.RejectsFile) //nolint:gosec // G705: stderr, not a browser context
	}
	if n := dependenciesOnRejected(result.SkippedDependencies, rejected); n > 0 {
		fmt.Fprintf(w, "Also skipped %d dependencies other records declare on them: to restore those too, fix the skipped records in the original input (or register the custom status or type they use) and re-import all of it\n", n)
	}
}

// dependenciesOnRejected counts the dependencies an import skipped (as it
// reports them: "<issue> -> <target>: <reason>") whose target is a rejected
// record: edges kept records declare on a skipped one. An edge is saved on
// the line of the record that declares it, so re-importing only the skipped
// lines would not restore these; re-importing the whole source once the
// records are valid does.
func dependenciesOnRejected(skippedDependencies []string, rejected []importRejection) int {
	targets := rejectedRecordIDs(rejected)
	n := 0
	for _, skipped := range skippedDependencies {
		_, edge, _ := strings.Cut(skipped, " -> ")
		target, _, _ := strings.Cut(edge, ": ")
		if _, onRejected := targets[target]; onRejected {
			n++
		}
	}
	return n
}

// declaredDependenciesOnRejected counts the dependencies the kept rows an
// import wrote (all but retiredIDs, which it skipped) declare on a rejected
// record no kept row writes, external targets (which the writer never looks
// up) aside. It is dependenciesOnRejected for an importer that reports no
// skipped dependencies and writes only into an empty database (the embedded
// auto-import), where each of these is an edge the writer drops for want of
// its target.
func declaredDependenciesOnRejected(kept []*types.Issue, rejected []importRejection, retiredIDs []string) int {
	targets := rejectedRecordIDs(rejected)
	retired := make(map[string]struct{}, len(retiredIDs))
	for _, id := range retiredIDs {
		retired[id] = struct{}{}
	}
	written := make([]*types.Issue, 0, len(kept))
	for _, issue := range kept {
		if issue == nil {
			continue
		}
		delete(targets, issue.ID)
		if _, skipped := retired[issue.ID]; !skipped {
			written = append(written, issue)
		}
	}
	n := 0
	for _, issue := range written {
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			// The writer's rule: an edge with no issue_id belongs to its row.
			from := dep.IssueID
			if from == "" {
				from = issue.ID
			}
			if _, onRejected := targets[dep.DependsOnID]; onRejected && !issueops.IsExternalDepTarget(from, dep.DependsOnID) {
				n++
			}
		}
	}
	return n
}

// rejectedRecordIDs returns the ids of the rejected rows. A title-only row
// has none, and nothing can depend on it.
func rejectedRecordIDs(rejected []importRejection) map[string]struct{} {
	ids := make(map[string]struct{}, len(rejected))
	for _, r := range rejected {
		if r.ID != "" {
			ids[r.ID] = struct{}{}
		}
	}
	return ids
}

// recoveryQuarantinePath is where a recovery import (bootstrap, init
// --from-jsonl, auto-import) sets aside the lines it skipped: beside the
// source, which it never modifies.
func recoveryQuarantinePath(source string) string {
	return source + ".rejected.jsonl"
}

// quarantineRecoveryRejects saves a recovery import's rejected lines to
// <source>.rejected.jsonl (replacing an earlier quarantine) and warns on
// stderr with the count, a representative line and reason, where they went,
// and how to restore them: fix them in the source (or register the custom
// status or type they use) and re-import the whole source, which also
// restores the dependencies other records declare on them; re-importing the
// quarantine alone would not, since an edge travels on the line of the record
// that declares it. With nothing rejected it does nothing, leaving any earlier
// quarantine alone. A quarantine that cannot be written is a warning, not a
// failure: the source still holds every line, and the valid rows are still
// restored. It returns the path written, or "" when nothing was saved.
func quarantineRecoveryRejects(w io.Writer, source string, rejected []importRejection) string {
	if len(rejected) == 0 {
		return ""
	}
	qpath := recoveryQuarantinePath(source)
	fmt.Fprintf(w, "warning: skipped %d invalid record(s) from %s; first: %s: %s\n", len(rejected), remedy.PrintedValue(source), rejected[0].describe(), rejected[0].Reason) //nolint:gosec // G705: stderr, not a browser context
	saved := qpath
	if err := writeImportRejects(qpath, rejected); err != nil {
		fmt.Fprintf(w, "warning: could not save the skipped lines to %s: %v; they are still in %s\n", remedy.PrintedValue(qpath), err, remedy.PrintedValue(source)) //nolint:gosec // G705: stderr, not a browser context
		saved = ""
	} else {
		fmt.Fprintf(w, "  the skipped lines were saved to %s\n", remedy.PrintedValue(qpath)) //nolint:gosec // G705: stderr, not a browser context
	}
	arg, note := remedy.CommandArg(runtime.GOOS, source, "<file>")
	fmt.Fprintf(w, "  to restore them, fix them in %s (or register the custom status or type they use) and re-import it: bd import %s%s\n", remedy.PrintedValue(source), arg, note) //nolint:gosec // G705: stderr, not a browser context
	return saved
}

// warnRecoveryDependenciesOnRejected follows a recovery import that skipped n
// dependencies other records declare on its rejected records
// (dependenciesOnRejected) with a warning that the re-import of source
// quarantineRecoveryRejects describes restores them too. n == 0 prints
// nothing.
func warnRecoveryDependenciesOnRejected(w io.Writer, source string, n int) {
	if n == 0 {
		return
	}
	fmt.Fprintf(w, "warning: also skipped %d dependencies other records declare on the skipped records; re-importing %s once they are fixed restores them\n", n, remedy.PrintedValue(source)) //nolint:gosec // G705: stderr, not a browser context
}
