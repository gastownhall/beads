package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/versionedhistory"
	"github.com/steveyegge/beads/issueops"
)

// The resolver for the versioned-history switch -- which planes it reads, why they
// are OR'd, how a store that cannot answer is handled -- lives in
// internal/versionedhistory, so that bd doctor's fix package (which cannot import
// package main) applies the same rule. This file keeps the two names the rest of
// cmd/bd uses.

// versionedHistorySettingKey is the one spelling of the switch, used for both the
// store-config read and the `bd config set` line the help text and the off-refusal
// tell people to run. They must not drift, so it is defined once, in the package
// that reads it.
const versionedHistorySettingKey = versionedhistory.ConfigKey

// versionedHistoryEnabled reports whether THIS STORE is recording versions: its
// own settings row, and nothing else. It is what `bd versions` calls, where the
// read is the point and no capability gate precedes it.
//
// It deliberately does not fold in the environment or config.yaml the way the
// rule for writers does. Those are process-wide and can only turn recording on
// for the writes this process makes; `bd versions` only reads, so they cannot
// make the store record anything. Letting them answer here made a store that
// never recorded read as one that was, and "No versions recorded yet" is the
// empty answer this command's three outcomes exist to prevent. What other
// clients of the store will do is what the store's row says.
func versionedHistoryEnabled(ctx context.Context, st storage.DoltStorage) bool {
	return versionedhistory.StoreSetting(ctx, st)
}

// versionedHistoryRefusalListLimit is how many offending issues the refusal spells
// out. The count it leads with is always the whole number.
const versionedHistoryRefusalListLimit = 20

// checkVersionedHistoryCanBeEnabled is the check `bd config set
// versioned-history.enabled true` and `bd config set-many
// versioned-history.enabled=true` make before they write the setting, and it
// makes it for that value alone: any other key, and every value that does not
// turn recording on (`false` included), passes without reading a row.
//
// With history on, recording a version runs in the same transaction as the write,
// so a write that introduces a number outside the I-JSON exact-integer range, in
// metadata or in a gate's timeout, already fails atomically. A row that holds one
// BEFORE the switch is turned on -- written while history was off, or by a path
// that does not record -- would fail every later write to it. So the switch reads
// every issue the store would version, runs the check recording runs over each
// one (issueops.FindUnversionable: the whole issue, not its metadata alone), and
// refuses, writing nothing, while any is refused.
//
// That check runs once, now. A writer that does not record (an older bd, bd sql, a
// pull from a clone that had history off) can add such a row at any time
// afterwards, even right after the switch is on, and the first write to it then
// fails with the same refusal, and the same fix applies. Making the check and the
// write of the setting one transaction would narrow that window, not close it.
//
// It reads through the same role accessor `bd list` does, so the direct and the
// proxied route both answer, and with the request that means "everything": every
// status, pinned rows, every type, and both planes (the scan itself skips the rows
// recording never versions). Limit 0 with no MaxRows is an unbounded read: a store
// larger than a page must not be checked on a page. A read that fails refuses the
// switch too, because a check that could not run has not passed. The read is a
// brief one, without the large text columns or the labels, and loads no
// dependencies; that is sound because none of those can hold a number, which the
// tests in internal/storage/issueops pin.
//
// On the direct route it reads through the store the command has already opened:
// `config set` opens it through openWorkspaceConfig before it asks, and the
// command's pre-run has opened it for a `config set-many` that names a database
// key, before the batch is validated. The proxied route reads through its provider.
//
// The environment and config.yaml planes do not pass through these commands, so
// they are not checked here; for those, recording's own refusal at write time is
// the control.
func checkVersionedHistoryCanBeEnabled(ctx context.Context, key, value string) error {
	if key != versionedHistorySettingKey || !versionedhistory.ValueEnables(value) {
		return nil
	}
	reader, err := openIssueReader()
	if err != nil {
		return fmt.Errorf("cannot check the store's issues before turning versioned history on: %w", err)
	}
	unlimited := 0
	page, err := reader.List(ctx, issueops.ListRequest{
		AllFlag:         true,
		IncludeAllTypes: true,
		Limit:           &unlimited,
		Brief:           true,
		SkipLabels:      true,
		SkipCounts:      true,
	})
	if err != nil {
		return fmt.Errorf("cannot check the store's issues before turning versioned history on: %w", err)
	}
	rows := make([]*types.Issue, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, item.Issue)
	}
	if found := storageissueops.FindUnversionable(rows); len(found) > 0 {
		return errors.New(unversionableIssueRefusal(found))
	}
	return nil
}

// unversionableIssueRefusal is the text of the refusal: how many issues, which
// (the first versionedHistoryRefusalListLimit), what is wrong with each and the
// field it is in, and the fix for each kind of field that is wrong. It says there
// is no override because there is none.
//
// The fix depends on the field. A number in metadata is replaced or removed with
// bd update, which works with history on or off, because a version is recorded
// from the state after the write. bd update cannot change a gate's timeout, so the
// gate is removed or its column is set with bd sql, while history is off; with
// history on, every write to that row, a close included, is refused. bd sql needs
// a server-backed store, which the text says.
func unversionableIssueRefusal(found []storageissueops.UnversionableIssue) string {
	noun, verb := "issues", "hold"
	if len(found) == 1 {
		noun, verb = "issue", "holds"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cannot turn versioned history on: %d %s %s a value a version could not record.\n", len(found), noun, verb)
	shown := found
	if len(shown) > versionedHistoryRefusalListLimit {
		shown = shown[:versionedHistoryRefusalListLimit]
	}
	for _, f := range shown {
		fmt.Fprintf(&b, "  %s  %v\n", f.ID, f.Err)
	}
	if len(found) > len(shown) {
		fmt.Fprintf(&b, "  ... and %d more\n", len(found)-len(shown))
	}
	var inMetadata, inTimeout bool
	for _, f := range found {
		switch f.Field {
		case "metadata", "":
			inMetadata = true
		case "timeout":
			inTimeout = true
		}
	}
	b.WriteString("With history on, any write to one of these issues that leaves its value in place would fail. Fix each one, then run this command again.\n")
	if inMetadata {
		b.WriteString("A number in metadata, one command per issue (this works with history on or off):\n")
		b.WriteString("  bd update <id> --metadata '{\"<key>\": \"<value as a string>\"}'   replace the value; a string keeps it exact\n")
		b.WriteString("  bd update <id> --unset-metadata <key>                              or remove the key\n")
	}
	if inTimeout {
		b.WriteString("A gate timeout is held as nanoseconds, and 9007199254740991 (about 104.25 days) is the most a version can record. bd update cannot change a gate's timeout. While history is off, remove the gate or set its timeout within that limit:\n")
		b.WriteString("  bd delete <id> --force                                             remove the gate; without --force bd only previews, and removing the gate also unblocks anything it was blocking\n")
		b.WriteString("  bd sql \"UPDATE issues SET timeout_ns = <nanoseconds> WHERE id = '<id>'\"   set the timeout; bd sql needs a server-backed store, not an embedded one\n")
	}
	b.WriteString("There is no override. This check runs once, now: if a writer that does not record (an older bd, bd sql, a pull from a clone that had history off) later adds such a row, the first write to it fails with the same refusal, and the same fix applies.")
	return b.String()
}
