package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// The import screen (GH#4492). One record the batch writer refuses, such as a
// row whose status the workspace never registered, used to abort the whole
// import: explicit `bd import` failed after it had already seeded the prefix,
// written the memories and committed earlier chunks, and auto-import restored
// nothing at all. The screen runs the writer's own validation over the write
// plan before any import write, so a caller decides up front what happens to
// the records the writer would refuse: explicit import refuses the input
// unless asked to skip them, and the recovery imports skip them into a
// quarantine file. The writer still validates every row it writes.

// importRecord is one issue line of an import source: the parsed issue, the
// line's 1-based physical line number and its original bytes, so a record the
// screen sets aside can be reported where it is and kept as it was.
type importRecord struct {
	Issue *types.Issue
	Line  int
	Raw   []byte
}

// importRejection is a record the screen set aside, with the writer's reason.
type importRejection struct {
	Line   int    `json:"line"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Raw    []byte `json:"-"`
}

// parseImportJSONL scans one JSONL stream into issue records and memory
// records. The optional _schema header and tombstones are skipped, and the
// "wisp_plane" marker (and the legacy "wisp" alias) are applied through
// applyImportWispPlane. A line that is not JSON, or does not decode as a
// record, stops the parse with its physical location in source: that is never
// a record the screen can set aside.
func parseImportJSONL(r io.Reader, source string) ([]importRecord, []memoryRecord, error) {
	scanner := bufio.NewScanner(r)
	// Allow up to 64MB per line for large descriptions.
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	var records []importRecord
	var memories []memoryRecord
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var peek map[string]json.RawMessage
		if err := json.Unmarshal(line, &peek); err != nil {
			return nil, nil, fmt.Errorf("%s:%d: failed to parse JSONL line: %w", source, lineNo, err)
		}

		// Skip the optional beads-jsonl header record (§J1.3). A canonical
		// export may prepend a provenance line, e.g.
		// {"_schema":"beads-jsonl/1","_dolt_branch":"main","_sort":"stable-v1"}.
		// It carries no _type and no issue fields; without this guard it would
		// unmarshal into an empty Issue and be refused for its missing title.
		if _, isHeader := peek["_schema"]; isHeader {
			continue
		}

		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal(line, &mem); err != nil {
					return nil, nil, fmt.Errorf("%s:%d: failed to parse memory record: %w", source, lineNo, err)
				}
				if mem.Key != "" && mem.Value != "" {
					memories = append(memories, mem)
				}
				continue
			}
		}

		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			return nil, nil, fmt.Errorf("%s:%d: failed to parse issue from JSONL: %w", source, lineNo, err)
		}
		// Skip tombstone entries: these are deleted issues exported by older
		// versions (pre-v0.50) with status "tombstone" and deleted_at set.
		// They are not valid for re-import since "tombstone" is not a real status.
		if issue.Status == "tombstone" {
			continue
		}
		applyImportWispPlane(peek, &issue)
		issue.SetDefaults()
		records = append(records, importRecord{Issue: &issue, Line: lineNo, Raw: slices.Clone(line)})
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("%s: failed to scan JSONL after line %d: %w", source, lineNo, err)
	}
	return records, memories, nil
}

// importRecordIssues returns the records' issues, in order.
func importRecordIssues(records []importRecord) []*types.Issue {
	issues := make([]*types.Issue, 0, len(records))
	for _, record := range records {
		issues = append(issues, record.Issue)
	}
	return issues
}

// keepImportRecords returns the records whose issue is in kept, in order: the
// records a filter over their issues (--dedup) left in.
func keepImportRecords(records []importRecord, kept []*types.Issue) []importRecord {
	keep := make(map[*types.Issue]struct{}, len(kept))
	for _, issue := range kept {
		keep[issue] = struct{}{}
	}
	out := make([]importRecord, 0, len(kept))
	for _, record := range records {
		if _, ok := keep[record.Issue]; ok {
			out = append(out, record)
		}
	}
	return out
}

// importVocabularyFunc reads the custom statuses and custom types the batch
// writer validates rows against.
type importVocabularyFunc func(ctx context.Context) (customStatuses, customTypes []string, err error)

// importVocabularyReader is the storage capability behind the classic stores'
// import vocabulary (EmbeddedDoltStore and DoltStore implement it): the
// writer's own resolvers, read in one transaction, with their errors.
type importVocabularyReader interface {
	ImportVocabulary(ctx context.Context) (customStatuses, customTypes []string, err error)
}

// storeImportVocabulary reads the import vocabulary of a classic store. A
// store without the capability is an error, not an empty vocabulary: screening
// against built-ins alone would set aside every row that uses a custom status.
func storeImportVocabulary(s storage.DoltStorage) importVocabularyFunc {
	return func(ctx context.Context) ([]string, []string, error) {
		reader, ok := s.(importVocabularyReader)
		if !ok {
			// The CLI's store is decorated (hooks, telemetry); the capability
			// lives on the backend underneath.
			reader, ok = storage.UnwrapStore(s).(importVocabularyReader)
		}
		if !ok {
			return nil, nil, fmt.Errorf("store %T cannot read the custom statuses and types an import is validated against", s)
		}
		return reader.ImportVocabulary(ctx)
	}
}

// uowImportVocabulary reads the import vocabulary through a proxied unit of
// work's config use case: the same status resolver and the same custom-type
// composition the writer's batch context uses, with their errors.
func uowImportVocabulary(uw uow.UnitOfWork) importVocabularyFunc {
	return func(ctx context.Context) ([]string, []string, error) {
		statuses, err := uw.ConfigUseCase().GetCustomStatuses(ctx)
		if err != nil {
			return nil, nil, err
		}
		customTypes, err := uw.ConfigUseCase().GetCustomTypes(ctx)
		if err != nil {
			return nil, nil, err
		}
		return types.CustomStatusNames(statuses), customTypes, nil
	}
}

// importScreen is an import's write plan after the screen.
type importScreen struct {
	// Write is the rows to hand the writer: the stale pre-filter's survivors
	// (every row under --allow-stale) less the rejected ones.
	Write []*types.Issue
	// Prefiltered reports that the stale pre-filter ran, so StaleSkippedIDs
	// and Plan are its outcome and the writer must not filter Write again.
	Prefiltered     bool
	StaleSkippedIDs []string
	Plan            importChangePlan
	// Rejected lists the records the writer would refuse, in file order.
	Rejected []importRejection
	rejected map[*types.Issue]struct{}
}

// Kept returns the records' issues the screen did not reject, in order.
func (s *importScreen) Kept(records []importRecord) []*types.Issue {
	kept := make([]*types.Issue, 0, len(records))
	for _, record := range records {
		if _, rejected := s.rejected[record.Issue]; !rejected {
			kept = append(kept, record.Issue)
		}
	}
	return kept
}

// screenImportRecords plans an import of records: the stale pre-filter first
// (unless allowStale), so a row the import would skip anyway is never
// screened, then the writer's validation over every row left to write. It
// reads the vocabulary only for a nonempty write set, and a failed read fails
// the screen: the rows are not invalid, the screen could not tell.
func screenImportRecords(ctx context.Context, lookup importIssueLookup, readVocabulary importVocabularyFunc, records []importRecord, allowStale bool) (*importScreen, error) {
	screen := &importScreen{Write: importRecordIssues(records), rejected: map[*types.Issue]struct{}{}}
	if !allowStale && len(screen.Write) > 0 {
		filtered, staleSkippedIDs, plan, err := filterStaleImportIssues(ctx, lookup, screen.Write)
		if err != nil {
			return nil, err
		}
		screen.Write, screen.StaleSkippedIDs, screen.Plan, screen.Prefiltered = filtered, staleSkippedIDs, plan, true
	}
	if len(screen.Write) == 0 {
		return screen, nil
	}

	customStatuses, customTypes, err := readVocabulary(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the custom statuses and types to validate the import against: %w", err)
	}
	recordOf := make(map[*types.Issue]importRecord, len(records))
	for _, record := range records {
		recordOf[record.Issue] = record
	}
	accepted := make([]*types.Issue, 0, len(screen.Write))
	var rejectedRows []*types.Issue
	for _, issue := range screen.Write {
		if err := issueops.ValidateIssueForInsert(issue, customStatuses, customTypes); err != nil {
			record := recordOf[issue]
			screen.Rejected = append(screen.Rejected, importRejection{Line: record.Line, ID: issue.ID, Reason: err.Error(), Raw: record.Raw})
			screen.rejected[issue] = struct{}{}
			rejectedRows = append(rejectedRows, issue)
			continue
		}
		accepted = append(accepted, issue)
	}
	screen.Write = accepted
	if screen.Prefiltered && len(rejectedRows) > 0 {
		dropRejectedFromImportPlan(&screen.Plan, rejectedRows, accepted)
	}
	return screen, nil
}

// dropRejectedFromImportPlan takes the rejected rows out of the pre-filter's
// change plan, so the report counts only rows the writer is handed. Each
// classified row contributed one entry: a would-create row to NewCount (and
// its id to NewIDs, deduplicated), an existing one to Updates or
// TieKeptLocal. An id stays listed while an accepted row still carries it.
func dropRejectedFromImportPlan(plan *importChangePlan, rejectedRows, accepted []*types.Issue) {
	newIDs := make(map[string]struct{}, len(plan.NewIDs))
	for _, id := range plan.NewIDs {
		newIDs[id] = struct{}{}
	}
	for _, issue := range rejectedRows {
		if _, isNew := newIDs[issue.ID]; isNew || issue.ID == "" {
			plan.NewCount--
			continue
		}
		if i := slices.IndexFunc(plan.Updates, func(c ImportChange) bool { return c.ID == issue.ID }); i >= 0 {
			plan.Updates = slices.Delete(plan.Updates, i, i+1)
			continue
		}
		if i := slices.Index(plan.TieKeptLocal, issue.ID); i >= 0 {
			plan.TieKeptLocal = slices.Delete(plan.TieKeptLocal, i, i+1)
		}
	}
	stillWritten := make(map[string]struct{}, len(accepted))
	for _, issue := range accepted {
		stillWritten[issue.ID] = struct{}{}
	}
	plan.NewIDs = slices.DeleteFunc(plan.NewIDs, func(id string) bool {
		_, ok := stillWritten[id]
		return !ok
	})
}

// importRejectionListLimit bounds the records an import names in its error or
// warning; the rest are counted.
const importRejectionListLimit = 10

// formatImportRejections lists up to importRejectionListLimit rejected
// records as "  source:line: id: reason" lines, then the remainder count.
func formatImportRejections(source string, rejected []importRejection) string {
	var b strings.Builder
	for i, r := range rejected {
		if i == importRejectionListLimit {
			fmt.Fprintf(&b, "  ... and %d more\n", len(rejected)-i) //nolint:gosec // G705: terminal text, not a browser context
			break
		}
		id := r.ID
		if id == "" {
			id = "(no id)"
		}
		fmt.Fprintf(&b, "  %s:%d: %s: %s\n", source, r.Line, id, r.Reason) //nolint:gosec // G705: terminal text, not a browser context
	}
	return b.String()
}

// importRejectedError is a strict import's refusal: what was refused, where,
// and the two ways forward.
func importRejectedError(source string, rejected []importRejection) error {
	return fmt.Errorf("%s has %d invalid record(s); nothing was imported by this import phase:\n%s"+
		"Repair the records, or register the values they use (e.g. bd config set status.custom ...), "+
		"or rerun with --skip-invalid --rejects <file> to import the valid records and keep the invalid ones in <file>",
		source, len(rejected), formatImportRejections(source, rejected))
}

// applyImportRejections settles the screen's rejected records for explicit
// `bd import`. Without --skip-invalid they refuse the import. With it they are
// reported in result, once in Skipped, and with --rejects saved to that file
// first: a rejects file that cannot be written refuses the import before any
// import write, so a skipped record is never dropped without the copy the
// caller asked for. A dry run (result.DryRun) settles them the same way but
// saves nothing.
func applyImportRejections(result *importResultJSON, source string, rejected []importRejection, skipInvalid bool, rejectsPath string) error {
	if len(rejected) == 0 {
		return nil
	}
	if !skipInvalid {
		return importRejectedError(source, rejected)
	}
	if rejectsPath != "" && !result.DryRun {
		if err := saveImportRejects(rejectsPath, source, rejected); err != nil {
			return fmt.Errorf("save the %d invalid record(s) from %s to %s; nothing was imported: %w", len(rejected), source, rejectsPath, err)
		}
		result.RejectsFile = rejectsPath
	}
	result.InvalidSkipped = len(rejected)
	result.InvalidRecords = rejected
	result.Skipped += len(rejected)
	return nil
}

// saveImportRejects adds the rejected records' original lines to the rejects
// file at path, one per line, keeping every record an earlier save left there
// and skipping a line the file already holds byte for byte. It refuses a path
// that is a symlink, a directory or another non-regular file, and one that is
// the import source itself (or a hardlink to it), since replacing either would
// lose data. The merged file goes through a same-directory temporary file that
// replaces path only once it is complete (mode 0600: the records are the
// workspace's issue data), so a failed save leaves the earlier file intact.
func saveImportRejects(path, source string, rejected []importRejection) error {
	var b bytes.Buffer
	seen := map[string]bool{}
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink; name a regular file", path)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	default:
		if sourceInfo, err := os.Stat(source); err == nil && os.SameFile(info, sourceInfo) {
			return fmt.Errorf("%s is the import source %s", path, source)
		}
		existing, err := os.ReadFile(path) //nolint:gosec // G304: the operator's rejects path
		if err != nil {
			return err
		}
		b.Write(existing)
		if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
			b.WriteByte('\n')
		}
		for _, line := range bytes.Split(existing, []byte("\n")) {
			seen[string(line)] = true
		}
	}
	for _, r := range rejected {
		line := bytes.TrimSuffix(r.Raw, []byte("\n"))
		if seen[string(line)] {
			continue
		}
		seen[string(line)] = true
		b.Write(line)
		b.WriteByte('\n')
	}
	return atomicWriteFile(path, b.Bytes())
}

// warnImportInvalidSkipped tells the operator, on stderr in every output mode,
// which records an import skipped as invalid and where they are kept.
func warnImportInvalidSkipped(source string, rejected []importRejection, rejectsPath string) {
	if len(rejected) == 0 {
		return
	}
	//nolint:gosec // G705: stderr, not a browser context
	fmt.Fprintf(os.Stderr, "warning: skipped %d invalid record(s) from %s:\n%s", len(rejected), source, formatImportRejections(source, rejected))
	switch {
	case rejectsPath != "":
		fmt.Fprintf(os.Stderr, "  rejected records saved to %s\n", rejectsPath) //nolint:gosec // G705: stderr, not a browser context
	case source == "stdin":
		fmt.Fprintln(os.Stderr, "  not saved separately; stdin is not kept, so rerun with --rejects <file> to keep them")
	default:
		fmt.Fprintf(os.Stderr, "  not saved separately; they remain in %s\n", source) //nolint:gosec // G705: stderr, not a browser context
	}
}

// warnImportInvalidWouldSkip is warnImportInvalidSkipped for a dry run, which
// skips the same records but saves none of them.
func warnImportInvalidWouldSkip(source string, rejected []importRejection, rejectsPath string) {
	if len(rejected) == 0 {
		return
	}
	//nolint:gosec // G705: stderr, not a browser context
	fmt.Fprintf(os.Stderr, "warning: dry run: would skip %d invalid record(s) from %s:\n%s", len(rejected), source, formatImportRejections(source, rejected))
	if rejectsPath != "" {
		fmt.Fprintf(os.Stderr, "  would save them to %s (a dry run writes nothing)\n", rejectsPath) //nolint:gosec // G705: stderr, not a browser context
	}
}

// parseImportJSONLFile parses the JSONL file at path (see parseImportJSONL).
func parseImportJSONLFile(path string) ([]importRecord, []memoryRecord, error) {
	//nolint:gosec // G304: the configured or user-named JSONL source
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read JSONL file %s: %w", path, err)
	}
	defer f.Close()
	return parseImportJSONL(f, path)
}

// importMemoryConfigEntries keys the memory records by their config key; a
// later record for a key wins.
func importMemoryConfigEntries(memories []memoryRecord) map[string]string {
	entries := make(map[string]string, len(memories))
	for _, mem := range memories {
		entries[kvPrefix+memoryPrefix+mem.Key] = mem.Value
	}
	return entries
}

// defaultImportRejectsPath is the quarantine a recovery import keeps the
// invalid records of source in: <dir>/<name>.rejected.jsonl for
// <dir>/<name>.jsonl, and source with .rejected.jsonl appended otherwise.
func defaultImportRejectsPath(source string) string {
	if base, ok := strings.CutSuffix(source, ".jsonl"); ok {
		return base + ".rejected.jsonl"
	}
	return source + ".rejected.jsonl"
}

// recoveryImportPlan is a recovery import's one parse and screen of its
// source (auto-import, `bd init --from-jsonl`, `bd bootstrap`): the issues to
// import, the memories as config entries, and the records skipped into the
// quarantine. The importers take the plan rather than the path, so none of
// them reads the source again and imports a snapshot the screen never saw.
type recoveryImportPlan struct {
	Source        string
	Issues        []*types.Issue
	ConfigEntries map[string]string
	// Rejected lists the records the screen skipped as invalid, saved to
	// RejectsPath before the plan was returned.
	Rejected    []importRejection
	RejectsPath string
}

// planRecoveryImport screens a recovery import's parsed source. A recovery
// imports what it can, so the records the writer would refuse are skipped
// into the quarantine next to source, which is saved before any import write.
// A failed vocabulary read or quarantine save fails the plan, and with it the
// import.
func planRecoveryImport(ctx context.Context, readVocabulary importVocabularyFunc, records []importRecord, memories []memoryRecord, source string) (*recoveryImportPlan, error) {
	plan := &recoveryImportPlan{Source: source, ConfigEntries: importMemoryConfigEntries(memories)}
	// No stale pre-filter here (lookup is unused under allowStale): the
	// writer's own stale handling still applies to the accepted rows.
	screen, err := screenImportRecords(ctx, nil, readVocabulary, records, true)
	if err != nil {
		return nil, err
	}
	plan.Issues = screen.Write
	if len(screen.Rejected) == 0 {
		return plan, nil
	}
	rejectsPath := defaultImportRejectsPath(source)
	if err := saveImportRejects(rejectsPath, source, screen.Rejected); err != nil {
		return nil, fmt.Errorf("save the %d invalid record(s) from %s to %s; nothing was imported: %w", len(screen.Rejected), source, rejectsPath, err)
	}
	plan.Rejected, plan.RejectsPath = screen.Rejected, rejectsPath
	return plan, nil
}

// planRecoveryImportFile parses the JSONL file at path and plans its recovery
// import (planRecoveryImport).
func planRecoveryImportFile(ctx context.Context, readVocabulary importVocabularyFunc, path string) (*recoveryImportPlan, error) {
	records, memories, err := parseImportJSONLFile(path)
	if err != nil {
		return nil, err
	}
	return planRecoveryImport(ctx, readVocabulary, records, memories, path)
}

// warnImportSkippedDependencies names, on stderr, the dependency edges a
// recovery import skipped, in bd import's words: an edge to a record the
// screen set aside, say.
func warnImportSkippedDependencies(skipped []string) {
	for _, edge := range skipped {
		fmt.Fprintf(os.Stderr, "Skipped dependency: %s\n", edge) //nolint:gosec // G705: stderr, not a browser context
	}
}
