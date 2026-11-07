package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
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
// never recorded read as one that was, and "No versions recorded" is the
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
// that does not record -- would, if it participates in history, fail every later
// write to it. So the switch reads every issue the store would version, runs the
// check recording runs over each one (issueops.FindUnversionable: the whole issue,
// not its metadata alone), and refuses, writing nothing, while any that
// participates, or any template, holds one.
//
// A write to a row is refused only if the row participates, so the scan asks the
// store which of the offenders participate (their participation_generation is set).
// Design §16.2b's write fence skips a legacy row, one created while history was off
// or by a writer that does not record, before the mint looks at it, so a value it
// holds blocks no write to that row. It can still block a write that copies it:
// `bd mol pour` and `bd mol bond` copy a proto's metadata and gate timeouts into
// every issue they create, and a new issue is versioned, so a pour or bond of a
// proto that holds such a value is refused. Two kinds of legacy issue are part of a
// proto, and the scan treats them differently:
//
//   - A template (types.Issue.IsTemplate, which `bd cook --persist` writes for every
//     issue of a proto it cooks) refuses the switch. bd update, the in-place fix a
//     user reaches for, refuses a template, and the pour's refusal names no fix, so
//     the switch refuses and names the fixes that do work: cooking the proto again
//     from a fixed formula (`bd cook --persist --force`), or `bd delete`.
//   - Any other issue of a proto, one under a template or an issue labeled
//     template (protoRoots), does not refuse the switch: the scan warns, naming the
//     issue and the proto's root. Such an issue can be fixed in place with history on or off (bd update,
//     bd delete for a gate, or `bd label remove <root> template`), and the label does
//     not tell a proto from a molecule poured from a stored proto, whose root carries
//     the proto's label (cloneSubgraphInto copies every label), so refusing over it
//     would block a store over a molecule that may never be poured again.
//
// Every other legacy row is passed over whatever it holds: on a store that has never
// recorded, every row is legacy, and only its templates can refuse the switch.
// Participation is not on types.Issue, so it is a second read
// (participatingIssueIDs), of the offenders alone, and the labels that mark a proto's
// root are a third, made only when a legacy offender that is not a template is left.
// A label read that fails turns the warning into one that says the scan could not
// tell; it refuses nothing, because nothing it could find would. A backend that
// serves the listing but cannot answer the participation read (one registered
// through backends.Lookup) keeps every offender, an over-approximation that can only
// refuse more, and the refusal says it could not tell. (Over HTTP the listing itself
// is refused today, ListRequest.IncludeAllTypes having no form on the v0 wire, so the
// switch cannot be turned on from an HTTP client at all.)
//
// warning, when not empty, is printed whether or not the switch is refused; err is the
// refusal.
//
// That check runs once, now. A writer that does not record (an older bd, bd sql, a
// pull from a clone that had history off) can add such a row at any time
// afterwards, even right after the switch is on. If the row participates in
// history, the first write to it then fails with the same refusal, and if it is
// part of a proto, so does the next pour or bond of that proto; the same fix
// applies. A legacy row is skipped by the write fence and stays writable. Making
// the check and the write of the setting one transaction would narrow that window,
// not close it.
//
// It reads through the same role accessor `bd list` does, so the direct and the
// proxied route both answer, and with the request that means "everything": every
// status, pinned rows, every type, and both planes (the scan itself skips the rows
// recording never versions). Limit 0 with no MaxRows is an unbounded read: a store
// larger than a page must not be checked on a page. A read that fails refuses the
// switch too, because a check that could not run has not passed. The read is a
// brief one: it skips the large text columns, the labels and the counts, which
// cannot hold a number, as the tests in internal/storage/issueops pin. It does load
// each row's parent and dependencies, which the mint canonicalizes and protoRoots
// walks.
//
// On the direct route it reads through the store the command has already opened:
// `config set` opens it through openWorkspaceConfig before it asks, and the
// command's pre-run has opened it for a `config set-many` that names a database
// key, before the batch is validated. The proxied route reads through its provider.
//
// The environment and config.yaml planes do not pass through these commands, so
// they are not checked here, and they are deliberately not checked when a store is
// opened either. Those planes are read by every bd process on every open, so a scan
// there would read every participating row on every command, a refusal there would
// also stop the `bd update` that fixes the row, and a warning would print on every
// invocation. Recording's own refusal at write time is the control on those planes:
// the mint refuses a write to a participating row this scan would name, and the
// pour or bond that copies a value from a proto issue, in the writer's own
// transaction, naming the issue (for a pour, the proto step it was copied from), the
// field and the value, and the fix the switch's refusal or warning prints applies to
// them.
func checkVersionedHistoryCanBeEnabled(ctx context.Context, key, value string) (warning string, err error) {
	if key != versionedHistorySettingKey || !versionedhistory.ValueEnables(value) {
		return "", nil
	}
	reader, err := openIssueReader()
	if err != nil {
		return "", fmt.Errorf("cannot check the store's issues before turning versioned history on: %w", err)
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
		return "", fmt.Errorf("cannot check the store's issues before turning versioned history on: %w", err)
	}
	rows := make([]*types.Issue, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, item.Issue)
	}
	found := storageissueops.FindUnversionable(rows)
	if len(found) == 0 {
		return "", nil
	}
	ids := make([]string, 0, len(found))
	for _, f := range found {
		ids = append(ids, f.ID)
	}
	participating, known, err := participatingIssueIDs(ctx, ids)
	if err != nil {
		return "", fmt.Errorf("cannot check the store's issues before turning versioned history on: %w", err)
	}
	templates := make(map[string]bool)
	for _, row := range rows {
		if row.IsTemplate {
			templates[row.ID] = true
		}
	}
	listed := make([]refusedIssue, 0, len(found))
	var legacy []storageissueops.UnversionableIssue
	for _, f := range found {
		if known && !templates[f.ID] {
			if _, ok := participating[f.ID]; !ok {
				// Legacy and not a template: a write to it is never refused, and a
				// pour or bond that copies it is fixed with bd update.
				legacy = append(legacy, f)
				continue
			}
		}
		listed = append(listed, refusedIssue{UnversionableIssue: f, template: templates[f.ID]})
	}
	if len(legacy) > 0 {
		legacyIDs := make([]string, 0, len(legacy))
		for _, f := range legacy {
			legacyIDs = append(legacyIDs, f.ID)
		}
		roots, rootErr := protoRoots(ctx, reader, page.Items, legacyIDs)
		warning = legacyProtoIssueWarning(legacy, roots, rootErr)
	}
	if len(listed) > 0 {
		return warning, errors.New(unversionableIssueRefusal(listed, known, warning != ""))
	}
	return warning, nil
}

// guardVersionedHistorySwitch runs checkVersionedHistoryCanBeEnabled for `bd config
// set` and `bd config set-many`: it prints the check's warning, if any, to stderr,
// and returns its refusal.
func guardVersionedHistorySwitch(ctx context.Context, key, value string) error {
	warning, err := checkVersionedHistoryCanBeEnabled(ctx, key, value)
	if warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	return err
}

// refusedIssue is one issue the switch's refusal lists: what a version could not
// record in it, and whether it is a template, which changes how it is fixed.
type refusedIssue struct {
	storageissueops.UnversionableIssue
	// template is set for a template (types.Issue.IsTemplate), which bd update
	// refuses to change.
	template bool
}

// protoRoots returns, for each of ids that `bd mol pour` and `bd mol bond` would
// copy if its proto were poured or bonded, the id of that proto's root. It finds
// them the way pour's loadTemplateSubgraph does, from each root down. A proto's
// root is a template (types.Issue.IsTemplate) or an issue labeled template. `bd mol
// pour` copies the subgraph under a root labeled template, and refuses a root
// without the label. `bd mol bond` treats any template operand as a proto
// (bondsAsProto), labeled or not, and on the direct route copies the whole
// subgraph under it (bondProtoMol loads it with loadTemplateSubgraph); only the
// proxied route's bond copies a template without the label alone. A root labeled
// template is a proto's root, each step created under it with --parent, which
// inherits the label, the root `bd cook --persist` writes, and the root of a
// molecule poured from a stored proto, which carries the proto's labels. Every
// issue under one, through the parent its row names, a parent-child dependency or a
// dotted id (<parent>.<n>), is part of it. As in pour's loadDescendants (GH#2476), a
// dotted id whose row has a parent-child dependency on another issue is not under
// its id's parent. An issue under more than one root is given the outermost one,
// the one a pour of the whole proto starts from.
//
// Everything but the labels is on items already: the listing carries each row's
// parent and its parent-child dependencies. The labels are one more read through
// the same reader, of the labeled issues alone. The walk starts from ids alone.
func protoRoots(ctx context.Context, reader issueops.Reader, items []*issueops.IssueWithCounts, ids []string) (map[string]string, error) {
	unlimited := 0
	labeled, err := reader.List(ctx, issueops.ListRequest{
		Labels:          []string{MoleculeLabel},
		AllFlag:         true,
		IncludeAllTypes: true,
		Limit:           &unlimited,
		Brief:           true,
		SkipLabels:      true,
		SkipCounts:      true,
	})
	if err != nil {
		return nil, fmt.Errorf("reading the issues labeled %s: %w", MoleculeLabel, err)
	}
	present := make(map[string]bool, len(items))
	for _, item := range items {
		present[item.Issue.ID] = true
	}
	parentsOf := make(map[string][]string)
	adopt := func(parent, child string) {
		if parent != "" && parent != child {
			parentsOf[child] = append(parentsOf[child], parent)
		}
	}
	seeds := make(map[string]bool)
	for _, item := range items {
		id := item.Issue.ID
		if item.Issue.IsTemplate {
			seeds[id] = true
		}
		var edgeParents []string
		if item.Parent != nil {
			edgeParents = append(edgeParents, *item.Parent)
		}
		for _, dep := range item.Issue.Dependencies {
			if dep != nil && dep.Type == types.DepParentChild {
				edgeParents = append(edgeParents, dep.DependsOnID)
			}
		}
		for _, parent := range edgeParents {
			adopt(parent, id)
		}
		if _, parent, depth := types.ParseHierarchicalID(id); depth > 0 && present[parent] {
			reparented := false
			for _, edgeParent := range edgeParents {
				if edgeParent != parent {
					reparented = true
					break
				}
			}
			if !reparented {
				adopt(parent, id)
			}
		}
	}
	for _, item := range labeled.Items {
		seeds[item.Issue.ID] = true
	}
	// Walk up from each issue to the farthest root above it (or itself), breadth
	// first, so a cycle of parents ends.
	roots := make(map[string]string)
	for _, id := range ids {
		root := ""
		if seeds[id] {
			root = id
		}
		seen := map[string]bool{id: true}
		level := []string{id}
		for len(level) > 0 {
			var next []string
			for _, at := range level {
				for _, parent := range parentsOf[at] {
					if seen[parent] {
						continue
					}
					seen[parent] = true
					next = append(next, parent)
				}
			}
			for _, at := range next {
				if seeds[at] {
					root = at
					break
				}
			}
			level = next
		}
		if root != "" {
			roots[id] = root
		}
	}
	return roots, nil
}

// legacyProtoIssueWarning is the text of the warning about legacy issues that are
// not templates and hold a value a version could not record: which of them are part
// of a proto, the root of each one's proto, and the fix. It names no issue that is
// part of no proto, and is empty when there is none. rootErr is a failed read of the
// labels: the warning then says the scan could not tell which of them are part of a
// proto.
func legacyProtoIssueWarning(legacy []storageissueops.UnversionableIssue, roots map[string]string, rootErr error) string {
	if rootErr != nil {
		return fmt.Sprintf("Warning: could not check whether a legacy issue holding a value a version could not record is part of a proto (%v). With history on, a persistent bd mol pour or bd mol bond of a proto holding one is refused when it is made, naming the issue; bd update fixes it, or, for a gate, bd delete.", rootErr)
	}
	type inProto struct {
		storageissueops.UnversionableIssue
		root string
	}
	var found []inProto
	inMetadata, inTimeout := false, false
	for _, f := range legacy {
		root, ok := roots[f.ID]
		if !ok {
			continue
		}
		found = append(found, inProto{f, root})
		switch f.Field {
		case "metadata", "":
			inMetadata = true
		case "timeout":
			inTimeout = true
		}
	}
	if len(found) == 0 {
		return ""
	}
	noun, verb := "issues", "hold"
	if len(found) == 1 {
		noun, verb = "issue", "holds"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Warning: %d legacy %s of a proto %s a value a version could not record. This does not stop versioned history from being turned on, and a write to such an issue is not refused, but bd mol pour copies its value into every issue it creates, and so does bd mol bond when the proto's root is a template; with history on each of those is versioned, so a persistent pour of its proto, or a bond of it when its root is a template, is refused until the value is fixed:\n", len(found), noun, verb)
	shown := found
	if len(shown) > versionedHistoryRefusalListLimit {
		shown = shown[:versionedHistoryRefusalListLimit]
	}
	for _, f := range shown {
		fmt.Fprintf(&b, "  %s  %v  (proto %s)\n", f.ID, f.Err, f.root)
	}
	if len(found) > len(shown) {
		fmt.Fprintf(&b, "  ... and %d more\n", len(found)-len(shown))
	}
	b.WriteString("A proto's root is a template or an issue labeled template. A molecule poured from a stored proto carries the proto's labels, so its root counts as one too: if you never pour it, nothing needs fixing, and bd label remove <root> template stops it counting as a proto (for an epic root, closing its last step then no longer closes it).\n")
	if inMetadata {
		b.WriteString("  bd update <id> --metadata '{\"<key>\": \"<value as a string>\"}'   replace a number in metadata; a string keeps it exact\n")
		b.WriteString("  bd update <id> --unset-metadata <key>                              or remove the key\n")
	}
	if inTimeout {
		b.WriteString("  bd delete <id> --force                                             remove a gate whose timeout is past 9007199254740991 ns (about 104.25 days); removing it also unblocks anything it was blocking\n")
		b.WriteString("  bd sql \"UPDATE issues SET timeout_ns = <nanoseconds> WHERE id = '<id>'\"   or set its timeout; bd sql needs a server-backed store, not an embedded one\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// participatingIssueIDs asks the route this invocation is on which of ids
// participate in versioned history. known is false when the route cannot tell, and
// the caller then keeps every id.
//
// The proxied route answers through a unit of work's IssueUseCase, which every
// decorator of the provider passes through; the direct route through the concrete
// store's storage.HistoryParticipation, peeled out of the decorator chain the way
// `bd versions` finds its VersionLister. Each runs issueops.ParticipatingIssueIDsInTx.
func participatingIssueIDs(ctx context.Context, ids []string) (participating map[string]struct{}, known bool, err error) {
	if usesProxiedServer() {
		if uowProvider == nil {
			return nil, false, errors.New("proxied-server UOW provider not initialized")
		}
		participating, err = uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (map[string]struct{}, error) {
			return uw.IssueUseCase().ParticipatingIssueIDs(ctx, ids)
		})
		return participating, err == nil, err
	}
	if store == nil {
		return nil, false, nil
	}
	reader, ok := storage.UnwrapStore(store).(storage.HistoryParticipation)
	if !ok {
		return nil, false, nil
	}
	participating, err = reader.ParticipatingIssueIDs(ctx, ids)
	return participating, err == nil, err
}

// unversionableIssueRefusal is the text of the refusal: how many issues, which
// (the first versionedHistoryRefusalListLimit), what is wrong with each and the
// field it is in, and the fix for each kind of issue and field. It says there is no
// override because there is none. participationKnown says whether the store told the
// scan which issues participate: when it did, every issue listed participates or is
// a template, a template is marked (template), and the text says legacy issues were
// otherwise left out; when it could not, the list may hold legacy issues too, and the
// text says that.
//
// The fix depends on the issue and the field. A template is read-only, so bd update
// refuses it whatever the field: its proto is cooked again from a fixed formula (bd
// cook --persist --force replaces it), or the template is removed. For any other
// issue, a number in metadata is replaced or removed with bd update, which works with
// history on or off, because a version is recorded from the state after the write.
// bd update cannot change a gate's timeout, so the gate is removed or its column is
// set with bd sql, while history is off; with history on, every write to that row, a
// close included, is refused if the row participates in history. bd sql needs a
// server-backed store, which the text says, and bd cook does not run in proxied-server
// mode, which it says too. A template loaded from a molecules.jsonl catalog is added
// back by the next command once deleted, so the text says to fix it in that file first.
// warned says whether a warning about legacy proto issues was printed above the
// refusal, which the text then points to.
func unversionableIssueRefusal(found []refusedIssue, participationKnown, warned bool) string {
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
		mark := ""
		if f.template {
			mark = "(template) "
		}
		fmt.Fprintf(&b, "  %s  %s%v\n", f.ID, mark, f.Err)
	}
	if len(found) > len(shown) {
		fmt.Fprintf(&b, "  ... and %d more\n", len(found)-len(shown))
	}
	var inMetadata, inTimeout, inTemplate bool
	for _, f := range found {
		if f.template {
			inTemplate = true
			continue
		}
		switch f.Field {
		case "metadata", "":
			inMetadata = true
		case "timeout":
			inTimeout = true
		}
	}
	if participationKnown {
		b.WriteString("Each issue listed participates in history, or is a template. One that participates was created while history was on, or entered the issues plane while history was on (a wisp promoted or made persistent), and with history on a write to it that leaves its value in place would be refused. One marked (template) is part of a proto: bd mol pour and bd mol bond copy its value into every issue they create, and with history on each of those is versioned, so every persistent pour or bond of that proto would be refused. Any other legacy issue, created while history was off or by a writer that does not record, is not versioned while recording stays on, so a write to it is not refused, and this check does not refuse over it.")
		if warned {
			b.WriteString(" One that is part of a proto is named in the warning above.")
		}
		b.WriteString(" Fix each one, then run this command again.\n")
	} else {
		b.WriteString("With history on, a write to one of these issues that leaves its value in place would be refused if the issue participates in history: it was created while history was on, or it entered the issues plane while history was on (a wisp promoted or made persistent). This store cannot tell the check which issues participate, so it also lists legacy issues, created while history was off or by a writer that does not record, whose writes would not be refused. Fix each one, then run this command again.\n")
	}
	if inTemplate {
		b.WriteString("A template, which bd cook --persist writes for every issue of a proto it cooks, is read-only, so bd update refuses it. Fix the value in the formula the proto was cooked from and cook it again, which replaces the proto, or remove the template:\n")
		b.WriteString("  bd cook <formula> --persist --force                                  replace the proto; bd cook does not run in proxied-server mode\n")
		b.WriteString("  bd delete <id> --force                                               remove the template from its proto; without --force bd only previews\n")
		b.WriteString("A template loaded from a molecules.jsonl catalog (.beads/, ~/.beads/ or $GT_ROOT/.beads/) is added back by the next command once it is gone, so fix it in that file, then delete it, and the fixed one is loaded.\n")
	}
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
	b.WriteString("There is no override. This check runs once, now: if a writer that does not record (an older bd, bd sql, a pull from a clone that had history off) later puts such a value into an issue that participates in history, or into an issue of a proto, the next write to that issue, or the next pour or bond of that proto, fails with the same refusal, and the same fix applies.")
	return b.String()
}
