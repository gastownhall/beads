package main

import (
	"context"
	"fmt"
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
	b.WriteString("nothing was imported. To import the valid records and set the invalid ones aside,\nre-run with --skip-invalid.")
	return fmt.Errorf("%s", b.String())
}
