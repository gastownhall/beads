package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// importRecordSource names the physical provenance of one parsed import
// record: its 1-based line number in the JSONL stream and its raw JSON bytes
// without the line terminator. Rejects are reported with the line and echoed
// into a rejects file from Raw verbatim — never re-serialized — so a repaired
// rejects file diffes cleanly against the source it came from.
type importRecordSource struct {
	Line int
	Raw  string
}

// importReject describes one record the pre-write screen excluded (GH#4492).
// Line and ID locate the record in its source file; Reason carries the
// validator's own error text, so the operator sees the same diagnosis the
// writer would have produced mid-transaction. raw (unexported, off the JSON
// wire) is the source line to echo into a rejects file.
type importReject struct {
	Line   int    `json:"line"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
	raw    string
}

// importVocabulary is the read seam behind the import pre-screen: the status
// and issue-type vocabularies the batch writer validates against, read the
// writer's own way (issueops.GetCustomStatusesTx / ResolveCustomTypesInTx)
// rather than from the cached, error-swallowing getters. Both concrete stores
// implement it (DoltStore.ReadInsertVocabulary,
// EmbeddedDoltStore.ReadInsertVocabulary); the proxied mode adapts its unit of
// work through uowImportVocabulary.
type importVocabulary interface {
	ReadInsertVocabulary(ctx context.Context) (statuses, customTypes []string, err error)
}

// importVocabularyOf resolves the vocabulary reader behind a classic-mode
// store. The CLI's store is decorated (hooks, telemetry), so a direct type
// assertion can miss; unwrap first the way excludeRetiredFromImportPlan does.
// Returns nil when nothing behind the store offers the capability — callers
// treat that as "cannot screen" rather than "nothing to screen".
func importVocabularyOf(s storage.DoltStorage) importVocabulary {
	if s == nil {
		return nil
	}
	if v, ok := s.(importVocabulary); ok {
		return v
	}
	if v, ok := storage.UnwrapStore(s).(importVocabulary); ok {
		return v
	}
	return nil
}

// uowImportVocabulary adapts the proxied unit of work's config use case to
// the screening seam: the proxied command has no store, and its vocabulary
// rides the same ConfigUseCase the proxied create path validates from, so the
// screen answers from the same source the writer does.
type uowImportVocabulary struct {
	uw uow.UnitOfWork
}

func (v uowImportVocabulary) ReadInsertVocabulary(ctx context.Context) ([]string, []string, error) {
	cfg := v.uw.ConfigUseCase()
	detailed, err := cfg.GetCustomStatuses(ctx)
	if err != nil {
		return nil, nil, err
	}
	customTypes, err := cfg.GetCustomTypes(ctx)
	if err != nil {
		return nil, nil, err
	}
	return types.CustomStatusNames(detailed), customTypes, nil
}

// screenImportRows runs the writer's own record validation over the planned
// import rows BEFORE any import write (GH#4492): every row must pass the same
// PrepareIssueForInsert checks the batch writer would run mid-transaction, so
// a bad record is refused with its line and reason up front instead of
// aborting a partially-committed import. Returned are the kept rows (same
// pointers, same order, rejects removed) and the rejects in file order.
//
// The vocabulary read is LAZY: only a row carrying a status or type outside
// the built-ins can be rejected over vocabulary, so a file of built-in records
// never pays the read. A store without the capability leaves the rows
// unscreened (kept, no rejects) — the writer's own validation remains the
// backstop. A vocabulary read that ERRORS is a hard stop for the whole import:
// screening against a guessed vocabulary would reject records the writer
// accepts, which is worse than the bug being fixed, so the error is reported
// with the line and id of the row that needed the vocabulary instead of
// rejecting that row (or any other).
func screenImportRows(ctx context.Context, vocab importVocabulary, issues []*types.Issue, sources map[*types.Issue]importRecordSource) ([]*types.Issue, []importReject, error) {
	var probe *types.Issue
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if !issue.Status.IsValid() || !issue.IssueType.IsValid() {
			probe = issue
			break
		}
	}
	var statuses, customTypes []string
	if probe != nil && vocab != nil {
		var err error
		statuses, customTypes, err = vocab.ReadInsertVocabulary(ctx)
		if err != nil {
			src := sources[probe]
			return nil, nil, fmt.Errorf("cannot read the status/type vocabulary to validate line %d (%s): %w", src.Line, probe.ID, err)
		}
	}
	kept := make([]*types.Issue, 0, len(issues))
	var rejects []importReject
	for _, issue := range issues {
		if issue == nil {
			kept = append(kept, issue)
			continue
		}
		if err := issueops.ValidateIssueForInsert(issue, statuses, customTypes); err != nil {
			src := sources[issue]
			rejects = append(rejects, importReject{
				Line:   src.Line,
				ID:     issue.ID,
				Reason: err.Error(),
				raw:    src.Raw,
			})
			continue
		}
		kept = append(kept, issue)
	}
	return kept, rejects, nil
}

// errStrictImportRejected is the error a strict (default) import returns for
// the first record its pre-write screen rejects. The wording is load-bearing
// (GH#4492): it names the line, the id and the validator's own reason, says
// plainly that NOTHING was imported — a user who believes some rows landed
// will repair the file and re-import, applying the good rows twice — and
// points at the escape hatch instead of making the operator re-read --help.
func errStrictImportRejected(reject importReject) error {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid record on line %d", reject.Line) //nolint:gosec // G705: plain-text error for the terminal, not HTML output
	if reject.ID != "" {
		fmt.Fprintf(&b, " (%s)", reject.ID) //nolint:gosec // G705: plain-text error for the terminal, not HTML output
	}
	fmt.Fprintf(&b, ": %s\n", reject.Reason) //nolint:gosec // G705: plain-text error for the terminal, not HTML output
	b.WriteString("nothing was imported. To import the valid records and set the invalid ones aside,\nre-run with --skip-invalid; add --rejects <file> to keep the skipped lines for repair.")
	return fmt.Errorf("%s", b.String())
}

// importRejectsAliasesInput reports whether the --rejects output names the
// import source itself — the one file the import is still reading from.
// Refusing the alias protects the operator from a repair loop that destroys
// its own input: the rejects file replaces whatever it names, and a source
// clobbered with only its invalid lines loses every valid record around them.
//
// Two checks, in widening order: a canonical-path comparison (EvalSymlinks
// resolves symlinks in the file and every directory on the way, and a
// not-yet-existing output falls back to Abs+Clean) and os.SameFile for paths
// that both exist but spell the same inode differently (hardlinks, and any
// symlink case the canonical pass could not resolve).
func importRejectsAliasesInput(rejectsPath, inputPath string) bool {
	if rejectsPath == "" || inputPath == "" {
		return false
	}
	if rejectsPath == inputPath {
		return true
	}
	canonical := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return filepath.Clean(p)
	}
	if canonical(rejectsPath) == canonical(inputPath) {
		return true
	}
	rejectsInfo, rejectsErr := os.Stat(rejectsPath)
	inputInfo, inputErr := os.Stat(inputPath)
	return rejectsErr == nil && inputErr == nil && os.SameFile(rejectsInfo, inputInfo)
}

// writeImportRejects atomically saves the rejected records' raw source lines
// to path: verbatim (never re-serialized), one per line in source order, so
// the file diffs cleanly against the source it came from and a repaired copy
// re-imports on its own. The temp-then-rename keeps a failed write from
// leaving a half-written file behind; an empty rejects list still writes an
// empty file, so the operator gets a definite artifact rather than silence.
func writeImportRejects(path string, rejects []importReject) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bd-import-rejects-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	writeErr := func() error {
		w := bufio.NewWriter(tmp)
		for _, reject := range rejects {
			if _, err := w.WriteString(reject.raw + "\n"); err != nil {
				return err
			}
		}
		return w.Flush()
	}()
	closeErr := tmp.Close()
	if writeErr != nil {
		os.Remove(tmpName) //nolint:gosec,errcheck // best-effort cleanup of our own temp file; the write error is what we report
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tmpName) //nolint:gosec,errcheck // best-effort cleanup of our own temp file; the close error is what we report
		return closeErr
	}
	if err := os.Chmod(tmpName, 0644); err != nil { //nolint:gosec // the rejects file is operator data, not a secret
		os.Remove(tmpName) //nolint:gosec,errcheck // best-effort cleanup of our own temp file; the chmod error is what we report
		return err
	}
	return os.Rename(tmpName, path) // atomic within the directory
}

// saveImportRejects writes the rejects file for an import running with
// --skip-invalid --rejects <path>, after the plan has settled the reject
// list and BEFORE any database change: whatever happens to the import next,
// the operator holds the artifact, and an unwritable path aborts the import
// while the database is still untouched. The saved path is recorded in the
// result only on success — the JSON surface must never claim a saved file
// that is not on disk.
func saveImportRejects(result *importResultJSON, rejects []importReject) error {
	if importRejects == "" || !importSkipInvalid {
		return nil
	}
	if err := writeImportRejects(importRejects, rejects); err != nil {
		return fmt.Errorf("cannot write the rejects file %s (nothing was imported): %w", importRejects, err)
	}
	result.RejectsFile = importRejects
	return nil
}

// importRejectsStderrLimit caps the per-run reject listing on stderr; past
// it the list says how many more there are instead of scrolling them all.
const importRejectsStderrLimit = 20

// reportImportRejects lists the records the screen skipped under
// --skip-invalid on stderr — line, id, the validator's own reason — with the
// total, the first 20, and where the skipped lines were saved. It runs in
// both --json and plain-text modes: the listing goes to stderr, so the JSON
// stream on stdout stays parseable.
func reportImportRejects(result importResultJSON) {
	if len(result.Rejected) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "Skipped %d invalid record(s):\n", len(result.Rejected)) //nolint:gosec // G705: stderr, not a browser context
	for i, reject := range result.Rejected {
		if i == importRejectsStderrLimit {
			fmt.Fprintf(os.Stderr, "  ... and %d more (run with --json or --rejects to see them all)\n", len(result.Rejected)-i) //nolint:gosec // G705: stderr, not a browser context
			break
		}
		if reject.ID != "" {
			fmt.Fprintf(os.Stderr, "  line %d (%s): %s\n", reject.Line, reject.ID, reject.Reason) //nolint:gosec // G705: stderr, not a browser context
		} else {
			fmt.Fprintf(os.Stderr, "  line %d: %s\n", reject.Line, reject.Reason) //nolint:gosec // G705: stderr, not a browser context
		}
	}
	if result.RejectsFile != "" {
		fmt.Fprintf(os.Stderr, "Rejected records saved to %s; repair them and re-import that file on its own.\n", result.RejectsFile) //nolint:gosec // G705: stderr, not a browser context
	}
}

// quarantineRecoveryRejects saves a screened recovery import's rejects to
// <source>.rejected.jsonl (GH#4492) and reports them: how many records were
// set aside, the first one's line/id/reason, where they were saved, and the
// repair hint. The quarantine is best-effort by design — a write failure only
// warns, naming both the file it could not write and the source, and never
// claims a save; the valid rows still restore. No rejects leaves any
// previous quarantine untouched, and the source itself is never rewritten.
// Returns the quarantine path actually written, "" when there was nothing
// to save or the save failed.
func quarantineRecoveryRejects(source string, rejects []importReject) string {
	if len(rejects) == 0 {
		return ""
	}
	quarantinePath := source + ".rejected.jsonl"
	first := rejects[0]
	if err := writeImportRejects(quarantinePath, rejects); err != nil {
		fmt.Fprintf(os.Stderr, "warning: skipped %d invalid record(s) from %s (first: line %d, %s: %s); could not save them to %s: %v\n",
			len(rejects), source, first.Line, first.ID, first.Reason, quarantinePath, err) //nolint:gosec // G705: stderr, not a browser context
		return ""
	}
	fmt.Fprintf(os.Stderr, "warning: skipped %d invalid record(s) from %s (first: line %d, %s: %s); saved to %s — repair them and re-import\n",
		len(rejects), source, first.Line, first.ID, first.Reason, quarantinePath) //nolint:gosec // G705: stderr, not a browser context
	return quarantinePath
}
