package storage

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/journalops"
)

// The COMPILE-TIME half, which fires before any test runs. A pointer type is
// identical to another only when the types it points at are identical, so each
// of these conversions is legal exactly while the name on the right is an alias
// of the one on the left.
//
// The pointer form is not decoration. For the three STRUCTS a plain assignment
// would do — two defined types with identical underlying types are not
// assignable to each other — but for the INTERFACE it would be blind, because
// interface-to-interface assignment goes by method set and a de-aliased
// EventsJournalCursor would still accept a journalops.Journal. One form that
// holds for all four is worth more than three that hold and a fourth that
// silently does not.
var (
	_ = func(p *journalops.Row) *EventsJournalRow { return p }
	_ = func(p *journalops.Page) *EventsJournalPage { return p }
	_ = func(p *journalops.TruncatedError) *EventsJournalTruncatedError { return p }
	_ = func(p *journalops.Journal) *EventsJournalCursor { return p }
)

// TestJournalNamesAreAliasesNotCopies pins the property the journal's move into
// a leaf package rests on: storage.EventsJournalRow, EventsJournalPage,
// EventsJournalTruncatedError and EventsJournalCursor are the journalops types,
// not lookalikes declared beside them.
//
// It exists because the two ways this breaks fail in OPPOSITE ways, and
// BUILDING THE TREE CATCHES NEITHER. Both readings below were measured, not
// reasoned about.
//
// THE INTERFACE BREAKS SILENTLY AND HARMLESSLY, which is what makes it easy to
// leave. Turning `type EventsJournalCursor = journalops.Journal` into
// `type EventsJournalCursor journalops.Journal` builds the WHOLE TREE clean —
// measured — because every implementation still satisfies both and interface
// satisfaction is structural. Nothing misbehaves at runtime; what is lost is the
// CLAIM, that one role has one name, and with it the guarantee that two callers
// holding the two spellings hold the same thing.
//
// THE ERROR IS WHERE IDENTITY IS BEHAVIORAL, and the dangerous shape is not the
// obvious one. `type EventsJournalTruncatedError journalops.TruncatedError`
// does not inherit Error(), so it stops being an error at every return site and
// the build fails loudly — that one takes care of itself. What compiles in
// silence is the COPY-PASTE TWIN: a struct declared here with the same fields
// and its own Error method. Every leg keeps returning it, every message reads
// identically, and errors.As against *journalops.TruncatedError stops matching
// for everything holding the leaf's spelling — the conformance tier, and any
// consumer downstream of it.
//
// TWO LAYERS CATCH THEM, and the order is worth knowing. The pins above fire
// FIRST, at compile time: with them in place neither break reaches a test run.
// The two tests below are the same properties stated as behavior, and they are
// what stands if the pins are ever removed — measured with them stripped, the
// twin reds BOTH (it is a different type, and it does not cross errors.As) and
// the de-aliased interface reds the first. Keeping both layers is deliberate:
// the pins say it soonest, and the errors.As test is the only place the
// property appears in the form a consumer actually depends on.
//
// The role contract asserts that errors.As half too
// (backend/conformance/journal_contract.go matches *journalops.TruncatedError
// against errors every leg constructs as *EventsJournalTruncatedError), but it
// only runs where a Dolt server or cgo does. These run everywhere, in
// milliseconds, and name the alias as the thing that broke.
func TestJournalNamesAreAliasesNotCopies(t *testing.T) {
	for _, alias := range []struct {
		name    string
		storage reflect.Type
		leaf    reflect.Type
	}{
		{"EventsJournalRow", reflect.TypeFor[EventsJournalRow](), reflect.TypeFor[journalops.Row]()},
		{"EventsJournalPage", reflect.TypeFor[EventsJournalPage](), reflect.TypeFor[journalops.Page]()},
		{"EventsJournalTruncatedError", reflect.TypeFor[EventsJournalTruncatedError](), reflect.TypeFor[journalops.TruncatedError]()},
		{"EventsJournalCursor", reflect.TypeFor[EventsJournalCursor](), reflect.TypeFor[journalops.Journal]()},
	} {
		if alias.storage != alias.leaf {
			t.Errorf("storage.%s is %s, not the journalops type %s: it is a redeclaration rather than an "+
				"alias, so every caller now has two vocabularies for one thing and errors.As matches only one",
				alias.name, alias.storage, alias.leaf)
		}
	}

	if EventsJournalTruncatedCode != journalops.TruncatedCode {
		t.Errorf("EventsJournalTruncatedCode = %q, want journalops.TruncatedCode (%q): the wire spelling of "+
			"a truncation cannot differ by which package a handler imported",
			EventsJournalTruncatedCode, journalops.TruncatedCode)
	}
}

// TestJournalTruncationCrossesTheAliasUnderErrorsAs is the runtime half, and the
// one that matches how the failure would actually present: a caller holding the
// leaf's spelling classifies an error a storage-side body constructed with the
// alias, through a wrapper, and gets its fields.
func TestJournalTruncationCrossesTheAliasUnderErrorsAs(t *testing.T) {
	wrapped := fmt.Errorf("reading the journal: %w",
		&EventsJournalTruncatedError{Since: 7, Floor: 12, Head: 40})

	var trunc *journalops.TruncatedError
	if !errors.As(wrapped, &trunc) {
		t.Fatalf("errors.As(%v, **journalops.TruncatedError) did not match an error built as "+
			"*storage.EventsJournalTruncatedError; the two spellings have to be one type", wrapped)
	}
	if trunc.Since != 7 || trunc.Floor != 12 || trunc.Head != 40 {
		t.Errorf("window = [%d..%d] after %d, want [12..40] after 7", trunc.Floor, trunc.Head, trunc.Since)
	}
}
