package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// importLocatedRecord is one parsed JSONL issue line with the facts a
// screening report needs: the 1-based PHYSICAL line the record came from
// and the record's original bytes, preserved verbatim so a quarantined
// reject keeps every unknown field it carried (GH#4492 design §1).
type importLocatedRecord struct {
	Issue *types.Issue
	Line  int
	Raw   string
}

// importRejection is one import row the screen refused: where it came from
// (physical line), which issue it named, and the bare validation reason.
type importRejection struct {
	Line   int
	ID     string
	Reason string
	Raw    string
}

// importInvalidRecord is the JSON-visible shape of one rejected record
// (importResultJSON.invalid_records).
type importInvalidRecord struct {
	Line   int    `json:"line"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// importRecordLineLimit bounds the per-record list a warning prints before
// collapsing the rest into a remainder count (GH#4492 design §3).
const importRecordLineLimit = 10

// parseImportRecordsLocated scans one JSONL stream into located issue
// records and memory records — the parse loop `bd import` and `bd import -`
// run through, in both storage modes. Record vocabulary (schema header,
// tombstones, memory records, the wisp-plane marker) is identical to
// parseJSONLFileLocated; only the memory representation differs
// ([]memoryRecord here, a prefixed config map there). Malformed JSON and
// record decoding errors are fatal and report the physical line, including
// under --skip-invalid: only rows that decoded cleanly can be screened,
// skipped and preserved (GH#4492 design §1/§7).
func parseImportRecordsLocated(r io.Reader, source string) ([]importLocatedRecord, []memoryRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	var records []importLocatedRecord
	var memories []memoryRecord
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var peek map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			return nil, nil, fmt.Errorf("%s: failed to parse JSONL line %d: %w", source, lineNo, err)
		}

		// Skip the optional beads-jsonl header record (§J1.3). A canonical
		// export may prepend a provenance line, e.g.
		// {"_schema":"beads-jsonl/1","_dolt_branch":"main","_sort":"stable-v1"}.
		// It carries no _type and no issue fields; without this guard it falls
		// through to the issue path, unmarshals into an empty Issue, and aborts
		// the whole import with "title is required".
		if _, isHeader := peek["_schema"]; isHeader {
			continue
		}

		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal([]byte(line), &mem); err != nil {
					return nil, nil, fmt.Errorf("%s: failed to parse memory record at line %d: %w", source, lineNo, err)
				}
				if mem.Key != "" && mem.Value != "" {
					memories = append(memories, mem)
				}
				continue
			}
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, nil, fmt.Errorf("%s: failed to parse issue from JSONL at line %d: %w", source, lineNo, err)
		}
		if issue.Status == "tombstone" {
			continue
		}
		applyImportWispPlane(peek, &issue)
		issue.SetDefaults()
		records = append(records, importLocatedRecord{Issue: &issue, Line: lineNo, Raw: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("%s: failed to scan JSONL: %w", source, err)
	}
	return records, memories, nil
}

// parseJSONLFileLocated is the recovery-reader twin of
// parseImportRecordsLocated (bootstrap, `bd init --from-jsonl`,
// auto-import), reading from a file and collecting memory records as the
// prefixed config map the recovery import writes. Physical line numbers and
// raw bytes ride along for the same reason; decoding errors stay fatal and
// name the line.
func parseJSONLFileLocated(path string) ([]importLocatedRecord, map[string]string, error) {
	//nolint:gosec // G304: path from user-provided CLI argument
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read JSONL file %s: %w", path, err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	// Allow up to 64MB per line for large descriptions
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var records []importLocatedRecord
	configEntries := make(map[string]string)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Peek at the record to check for _type field
		var peek map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			return nil, nil, fmt.Errorf("%s: failed to parse JSONL line %d: %w", path, lineNo, err)
		}

		// Skip the optional beads-jsonl metadata/header record. Canonical
		// exports prepend a schema+provenance line; identified by the _schema
		// sentinel, which real issue/memory records never carry.
		if _, isHeader := peek["_schema"]; isHeader {
			continue
		}

		// Check if this is a memory record
		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal([]byte(line), &mem); err != nil {
					return nil, nil, fmt.Errorf("%s: failed to parse memory record at line %d: %w", path, lineNo, err)
				}
				if mem.Key != "" && mem.Value != "" {
					configEntries[kvPrefix+memoryPrefix+mem.Key] = mem.Value
				}
				continue
			}
		}

		// Regular issue record
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, nil, fmt.Errorf("%s: failed to parse issue from JSONL at line %d: %w", path, lineNo, err)
		}
		// Skip tombstone entries: these are deleted issues exported by older
		// versions (pre-v0.50) with status "tombstone" and deleted_at set.
		// They are not valid for re-import since "tombstone" is not a real status.
		if issue.Status == "tombstone" {
			continue
		}

		applyImportWispPlane(peek, &issue)

		issue.SetDefaults()
		records = append(records, importLocatedRecord{Issue: &issue, Line: lineNo, Raw: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("%s: failed to scan JSONL: %w", path, err)
	}

	return records, configEntries, nil
}

// importVocabularyReader is the OPTIONAL storage capability an import screen
// reads the writer's supported custom status/type vocabulary through
// (GH#4492 design §2). It is implemented by both classic adapters
// (embedded and server-mode Dolt) and deliberately propagates resolver
// errors where the public getters mask them behind config.yaml fallbacks: a
// masked read failure would screen every configured-vocabulary row as
// invalid. The proxied-server route reads the same vocabulary through its
// UOW's ConfigUseCase instead. No store, no screen: a store without the
// capability aborts the import before any database or quarantine write
// rather than guessing the vocabulary.
type importVocabularyReader interface {
	ReadImportVocabulary(ctx context.Context) (customStatuses, customTypes []string, err error)
}

// classicImportVocabulary resolves the screen vocabulary for the classic
// (embedded or server-mode) store, unwrapping the CLI's decorators the same
// way the retired-id lookup does. A missing capability is an error, not a
// silent empty vocabulary: screening against "no custom vocabulary" would
// misclassify rows the writer would accept. It returns the closure shape
// applyExplicitImportScreen takes; the context is already captured.
func classicImportVocabulary(ctx context.Context, s storage.DoltStorage) func(context.Context) ([]string, []string, error) {
	return func(context.Context) ([]string, []string, error) {
		reader, ok := s.(importVocabularyReader)
		if !ok {
			// The CLI's store is decorated (hooks, telemetry); the capability
			// lives on the backend underneath.
			if unwrapped := storage.UnwrapStore(s); unwrapped != nil {
				reader, ok = unwrapped.(importVocabularyReader)
			}
		}
		if !ok {
			return nil, nil, fmt.Errorf("store %T does not support reading the import vocabulary; refusing to screen rows against an unknown status/type vocabulary", s)
		}
		statuses, issueTypes, err := reader.ReadImportVocabulary(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("read import vocabulary: %w", err)
		}
		return statuses, issueTypes, nil
	}
}

// screenImportRecords classifies parsed records against the writer's own
// insert validation, using the writer's vocabulary (custom statuses and
// types included). Kept rows are returned in file order for the writer;
// refused rows keep their line, id, reason and raw bytes. The screen never
// mutates the records: the writer revalidates inside its transaction, so a
// configuration change between screen and write still fails at the write.
func screenImportRecords(records []importLocatedRecord, customStatuses, customTypes []string) (kept []*types.Issue, rejects []importRejection) {
	kept = make([]*types.Issue, 0, len(records))
	for _, rec := range records {
		if err := issueops.ValidateIssueForInsertScreen(rec.Issue, customStatuses, customTypes); err != nil {
			rejects = append(rejects, importRejection{Line: rec.Line, ID: rec.Issue.ID, Reason: err.Error(), Raw: rec.Raw})
			continue
		}
		kept = append(kept, rec.Issue)
	}
	return kept, rejects
}

// importScreenReport is what a screen handed back to the command: the
// refused rows (in file order) and, when they were preserved, the rejects
// file they were written to.
type importScreenReport struct {
	Invalid []importRejection
	SavedTo string
}

// applyExplicitImportScreen runs the explicit-import screening policy
// (GH#4492 design §3) over the parsed records and returns the rows the
// writer may import. Strict mode (the default) refuses the whole import —
// nonzero, nothing written — naming each rejected row's source line, id and
// reason plus the remedy; --skip-invalid drops the rejected rows instead,
// preserving them in --rejects before any database write (a rejects failure
// imports nothing). --dry-run keeps the same classification and exit
// semantics while writing neither database nor rejects file. The vocabulary
// read happens once, only for a nonempty plan, and its failure aborts the
// import before any write rather than misclassifying rows (design §2).
func applyExplicitImportScreen(ctx context.Context, records []importLocatedRecord, source string, readVocabulary func(context.Context) ([]string, []string, error)) ([]*types.Issue, importScreenReport, error) {
	var report importScreenReport
	if len(records) == 0 {
		return nil, report, nil
	}

	customStatuses, customTypes, err := readVocabulary(ctx)
	if err != nil {
		return nil, report, fmt.Errorf("import screen: %w", err)
	}

	kept, rejects := screenImportRecords(records, customStatuses, customTypes)
	if len(rejects) == 0 {
		return kept, report, nil
	}
	report.Invalid = rejects

	if !importSkipInvalid {
		return nil, report, strictImportRefusalError(source, rejects)
	}

	if importDryRun {
		printImportRejectionWarnings(source, rejects, "")
		return kept, report, nil
	}

	if importRejectsPath == "" {
		// Explicit skip without a rejects path is allowed, but the warning
		// must say the records were not saved separately and name the input
		// they came from (design §3).
		printImportRejectionWarnings(source, rejects, "")
		return kept, report, nil
	}

	if err := appendImportRejects(importRejectsPath, source, rejectionRawLines(rejects)); err != nil {
		return nil, report, fmt.Errorf("save rejected records: %w", err)
	}
	report.SavedTo = importRejectsPath
	printImportRejectionWarnings(source, rejects, importRejectsPath)
	return kept, report, nil
}

// rejectionRawLines returns the rejected records' original bytes, in file
// order, for the rejects writer.
func rejectionRawLines(rejects []importRejection) []string {
	lines := make([]string, 0, len(rejects))
	for _, r := range rejects {
		lines = append(lines, r.Raw)
	}
	return lines
}

// strictImportRefusalError is what strict `bd import` fails with when the
// screen refused at least one row: every rejection's source line, id and
// reason (bounded, with a remainder count), the statement that nothing was
// imported by this import phase, and the remedies — repair or register the
// invalid value, or re-run with --skip-invalid (design §3). It deliberately
// does not claim any initialization or pre-run setup was rolled back.
func strictImportRefusalError(source string, rejects []importRejection) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "refusing to import %s: %d invalid record(s) found; nothing was imported by this import phase", source, len(rejects)) //nolint:gosec // G705: stderr-bound error text, not a browser context
	for i, r := range rejects {
		if i >= importRecordLineLimit {
			fmt.Fprintf(&sb, "\n  ... and %d more", len(rejects)-i) //nolint:gosec // G705: stderr-bound error text, not a browser context
			break
		}
		fmt.Fprintf(&sb, "\n  line %d (id %s): %s", r.Line, r.ID, r.Reason) //nolint:gosec // G705: stderr-bound error text, not a browser context
	}
	sb.WriteString("\nrepair or register the invalid value, or re-run with --skip-invalid to import the valid records (pair with --rejects <file> to preserve the rejected ones)")
	return errors.New(sb.String())
}

// printImportRejectionWarnings writes the bounded skip warning to stderr:
// the count, where the records came from, where (if anywhere) they were
// preserved, and up to importRecordLineLimit per-record lines with a
// remainder count. Every line starts with "warning: " so --quiet-adjacent
// consumers (the legacy git-hook sync importer) can forward exactly these
// lines while staying silent about normal output (design §3/§4).
func printImportRejectionWarnings(source string, rejects []importRejection, savedTo string) {
	if len(rejects) == 0 {
		return
	}
	if savedTo != "" {
		fmt.Fprintf(os.Stderr, "warning: import skipped %d invalid record(s) from %s; preserved in %s\n", len(rejects), source, savedTo) //nolint:gosec // G705: stderr, not a browser context
	} else {
		fmt.Fprintf(os.Stderr, "warning: import skipped %d invalid record(s) from %s; they were not saved separately (re-run with --rejects <file> to preserve them)\n", len(rejects), source) //nolint:gosec // G705: stderr, not a browser context
	}
	for i, r := range rejects {
		if i >= importRecordLineLimit {
			fmt.Fprintf(os.Stderr, "warning:   ... and %d more\n", len(rejects)-i) //nolint:gosec // G705: stderr, not a browser context
			break
		}
		fmt.Fprintf(os.Stderr, "warning:   line %d (id %s): %s\n", r.Line, r.ID, r.Reason) //nolint:gosec // G705: stderr, not a browser context
	}
}

// importRejectionsJSON renders the report's rejected rows in the structured
// shape importResultJSON.invalid_records carries.
func importRejectionsJSON(rejects []importRejection) []importInvalidRecord {
	out := make([]importInvalidRecord, 0, len(rejects))
	for _, r := range rejects {
		out = append(out, importInvalidRecord{Line: r.Line, ID: r.ID, Reason: r.Reason})
	}
	return out
}

// filterDuplicateRecordsByTitle is the located-records wrapper around
// filterDuplicatesByTitle: it keeps the --dedup classification on the issue
// rows while preserving each kept row's line and raw bytes for the screen
// and the rejects writer.
func filterDuplicateRecordsByTitle(ctx context.Context, st importTitleSearcher, records []importLocatedRecord) ([]importLocatedRecord, int) {
	if len(records) == 0 {
		return records, 0
	}
	issues := make([]*types.Issue, 0, len(records))
	for _, rec := range records {
		issues = append(issues, rec.Issue)
	}
	keptIssues, hits := filterDuplicatesByTitle(ctx, st, issues)
	if hits == 0 && len(keptIssues) == len(issues) {
		return records, 0
	}
	keptSet := make(map[*types.Issue]struct{}, len(keptIssues))
	for _, issue := range keptIssues {
		keptSet[issue] = struct{}{}
	}
	kept := make([]importLocatedRecord, 0, len(keptIssues))
	for _, rec := range records {
		if _, ok := keptSet[rec.Issue]; ok {
			kept = append(kept, rec)
		}
	}
	return kept, hits
}

// planRecoveryJSONLImport parses a recovery-import source ONCE (bootstrap,
// `bd init --from-jsonl`, auto-import), screens it with the writer's
// vocabulary, and quarantines every rejected record before the caller
// touches the database (GH#4492 design §1/§4). A recovery import is always
// skip + mandatory quarantine: it is the path a broken mirror must not be
// able to wedge, so rejected rows are preserved next to the source and the
// valid rows imported. The quarantine write must succeed before the import
// writes; a vocabulary-read failure aborts with no data and no quarantine
// write rather than misclassifying rows.
func planRecoveryJSONLImport(ctx context.Context, store storage.DoltStorage, sourcePath string) (*recoveredJSONLPlan, error) {
	records, configEntries, err := parseJSONLFileLocated(sourcePath)
	if err != nil {
		return nil, err
	}

	plan := &recoveredJSONLPlan{
		Memories: configEntries,
		Source:   sourcePath,
	}
	if len(records) == 0 {
		return plan, nil
	}

	customStatuses, customTypes, err := classicImportVocabulary(ctx, store)(ctx)
	if err != nil {
		return nil, fmt.Errorf("recovery import screen: %w", err)
	}

	kept, rejects := screenImportRecords(records, customStatuses, customTypes)
	plan.Issues = kept
	if len(rejects) > 0 {
		quarantine := defaultImportRejectsPath(sourcePath)
		if err := appendImportRejects(quarantine, sourcePath, rejectionRawLines(rejects)); err != nil {
			return nil, fmt.Errorf("quarantine rejected records: %w", err)
		}
		plan.Rejected = rejects
		plan.Quarantine = quarantine
		printImportRejectionWarnings(sourcePath, rejects, quarantine)
	}
	return plan, nil
}

// recoveredJSONLPlan is a parsed-and-screened recovery import: the rows the
// screen accepted, the memory records (as the prefixed config map the
// recovery paths write), and — when anything was rejected — the rejects and
// the quarantine file they were preserved in. Callers import ONLY the kept
// rows; the plan is the single parse, so no path re-reads and screens a
// different snapshot of the source (design §1).
type recoveredJSONLPlan struct {
	Issues     []*types.Issue
	Memories   map[string]string
	Source     string
	Quarantine string
	Rejected   []importRejection
}
