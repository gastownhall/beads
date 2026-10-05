//go:build cgo

package embeddeddolt_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// metadataNumberCorpus is what TestVersionedHistoryKeepsNumbersTheStoreDistinguishesApart writes
// through the store. It groups literals that share one nearest binary64 (0.1 and its longer
// spellings, 1 and its spellings, pi at several precisions), because those are the literals a
// store could tell apart and RFC 8785 cannot, and it includes the values outside the I-JSON
// exact-integer range that the mint has to refuse. The server leg carries the same list in
// dolt/metadata_number_fidelity_test.go; keep the two in step.
var metadataNumberCorpus = []string{
	"0.1", "0.10000000000000000555", "0.1000000000000000055511151231257827",
	"1", "1.0", "1.00", "1e0", "10e-1",
	"3.14159265358979323846", "3.141592653589793", "3.1415926535897932",
	"0.3", "0.30000000000000004",
	"0", "-0", "0.0", "1e-400",
	"9007199254740991",
	"9007199254740992", "9007199254740993", "9007199254740993.5", "9007199254740994",
	"1727000000000000000", "1727000000000000123",
	"1e300", "123456789012345678901234567890",
}

// metadataNumbersOutsideTheRange are the corpus literals no store normalization can bring back
// inside 2^53-1: each is refused at the mint, and the write it belonged to must not survive.
var metadataNumbersOutsideTheRange = map[string]bool{
	"9007199254740992": true, "9007199254740993": true, "9007199254740993.5": true, "9007199254740994": true,
	"1727000000000000000": true, "1727000000000000123": true,
	"1e300": true, "123456789012345678901234567890": true,
}

// embeddedDoltVersion names the engine this leg runs. The embedded engine answers
// dolt_version() with the placeholder "SET_BY_INIT", and a test binary's build info lists no
// dependencies, so the version of the module it is built from is read from go.mod; both are
// logged. Where go.mod is not reachable the reported string stands alone.
func embeddedDoltVersion(reported string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return reported
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "go.mod"))
	if err != nil {
		return reported
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "github.com/dolthub/dolt/go" {
			return fields[1] + " (go.mod; dolt_version() reports " + reported + ")"
		}
	}
	return reported
}

// TestVersionedHistoryKeepsNumbersTheStoreDistinguishesApart is the embedded leg of the safety
// property the number-admission rule rests on; the server leg's twin carries the full argument.
//
// In short: RFC 8785 turns every number into its nearest double, so two literals that differ by
// less than a double can share a canonical form, which is harmless only while the store cannot
// hold them apart. Whatever this Dolt does to numbers, it asserts in three phases that (1) with
// history OFF the store accepts a number outside the I-JSON range and the table logs what it
// holds, (2) with history ON a later write to such a row is refused and changes nothing, and
// (3) no two literals which read back as DIFFERENT numbers were both admitted with the SAME
// canonical form, and a literal the mint refuses leaves neither an issue row nor a version row.
func TestVersionedHistoryKeepsNumbersTheStoreDistinguishesApart(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "nf")
	ctx := t.Context()
	kit := newEmbeddedRoleFixtureKit(te, "nf")

	configurer, ok := any(te.store).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", te.store)
	}

	var reported string
	if err := kit.QueryScalar(ctx, `SELECT dolt_version()`, nil, &reported); err != nil {
		reported = "unknown (" + err.Error() + ")"
	}
	newIssue := func(id, lit string) *types.Issue {
		return &types.Issue{
			ID: id, Title: "n " + lit, IssueType: types.TypeTask, Status: types.StatusOpen,
			Metadata: json.RawMessage(`{"n":` + lit + `}`),
		}
	}

	// Phase 1: history off. What does the store hold for a number outside the range?
	t.Logf("metadata number normalization, embedded leg, dolt %s", embeddedDoltVersion(reported))
	configurer.SetVersionedHistoryEnabled(false)
	var held []string
	for i, lit := range metadataNumberCorpus {
		if !metadataNumbersOutsideTheRange[lit] {
			continue
		}
		id := fmt.Sprintf("nf-off-%02d", i)
		if err := te.store.CreateIssue(ctx, newIssue(id, lit), "actor"); err != nil {
			t.Logf("  history off: %-34s the store refused it: %v", lit, err)
			continue
		}
		issue, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("reading %s back: %v", id, err)
		}
		stored, _ := storedNumberAndCanonicalMetadata(t, issue.Metadata, nil)
		t.Logf("  history off: %-34s held as %s", lit, stored)
		held = append(held, id)
	}

	// Phase 2: history on. A write to a row holding such a number is refused and changes nothing.
	configurer.SetVersionedHistoryEnabled(true)
	for _, id := range held {
		before, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("reading %s: %v", id, err)
		}
		err = te.store.UpdateIssue(ctx, id, map[string]any{"title": "retitled"}, "actor")
		if !errors.Is(err, issueops.ErrIntegerNotRepresentable) {
			t.Errorf("updating %s, which holds a number outside the I-JSON range, = %v, want ErrIntegerNotRepresentable", id, err)
		}
		after, getErr := te.store.GetIssue(ctx, id)
		if getErr != nil || after.Title != before.Title {
			t.Errorf("the refused update of %s left a trace: title %q -> %q (err %v)", id, before.Title, after.Title, getErr)
		}
	}

	// Phase 3: history on, fresh rows. The property.
	type admittedLiteral struct{ written, storedAs, canonical string }
	var admitted []admittedLiteral
	for i, lit := range metadataNumberCorpus {
		id := fmt.Sprintf("nf-%02d", i)
		err := te.store.CreateIssue(ctx, newIssue(id, lit), "actor")
		if err != nil {
			if !metadataNumbersOutsideTheRange[lit] {
				t.Errorf("%s was refused although it is an ordinary number: %v", lit, err)
			} else if !errors.Is(err, issueops.ErrIntegerNotRepresentable) {
				t.Errorf("%s was refused with %v, want ErrIntegerNotRepresentable", lit, err)
			}
			if _, getErr := te.store.GetIssue(ctx, id); !errors.Is(getErr, storage.ErrNotFound) {
				t.Errorf("%s was refused but its issue exists afterwards (GetIssue = %v): the refusal did not abort the write", lit, getErr)
			}
			var versions int
			if scanErr := kit.QueryScalar(ctx, `SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?`, []any{id}, &versions); scanErr != nil || versions != 0 {
				t.Errorf("%s was refused but left %d version rows (err %v)", lit, versions, scanErr)
			}
			t.Logf("  history on:  %-34s refused", lit)
			continue
		}
		if metadataNumbersOutsideTheRange[lit] {
			t.Errorf("%s is outside the I-JSON exact-integer range and was written", lit)
		}
		issue, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("reading %s back: %v", id, err)
		}
		var durable []byte
		if err := kit.QueryScalar(ctx,
			`SELECT durable_state FROM issue_versions WHERE issue_id = ? ORDER BY revision DESC LIMIT 1`, []any{id}, &durable); err != nil {
			t.Fatalf("reading the version of %s: %v", id, err)
		}
		stored, canonical := storedNumberAndCanonicalMetadata(t, issue.Metadata, durable)
		t.Logf("  history on:  %-34s read back as %-28s canonical metadata %s", lit, stored, canonical)
		admitted = append(admitted, admittedLiteral{lit, stored, canonical})
	}

	if len(admitted) < 12 {
		t.Fatalf("only %d of %d literals were admitted; the property is close to vacuous", len(admitted), len(metadataNumberCorpus))
	}
	first := map[string]admittedLiteral{}
	for _, a := range admitted {
		prior, seen := first[a.canonical]
		if !seen {
			first[a.canonical] = a
			continue
		}
		if !sameNumber(prior.storedAs, a.storedAs) {
			t.Errorf("literals %s and %s read back from the store as different numbers (%s and %s) but share the canonical form %s: "+
				"two distinct stored states would carry one content token", prior.written, a.written, prior.storedAs, a.storedAs, a.canonical)
		}
	}
}

// storedNumberAndCanonicalMetadata returns the number the store handed back in the issue's
// metadata, and, when durable is given, the metadata member of the canonical bytes the mint
// stored for it.
func storedNumberAndCanonicalMetadata(t *testing.T, readBack json.RawMessage, durable []byte) (stored, canonical string) {
	t.Helper()
	var back struct {
		N json.Number `json:"n"`
	}
	dec := json.NewDecoder(bytes.NewReader(readBack))
	dec.UseNumber()
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("decoding the metadata read back %s: %v", readBack, err)
	}
	if durable == nil {
		return back.N.String(), ""
	}
	var version struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(durable, &version); err != nil {
		t.Fatalf("decoding durable_state %s: %v", durable, err)
	}
	return back.N.String(), strings.TrimSpace(string(version.Metadata))
}

// sameNumber reports whether two JSON number literals denote the same value, compared exactly.
func sameNumber(a, b string) bool {
	x, okA := new(big.Rat).SetString(a)
	y, okB := new(big.Rat).SetString(b)
	return okA && okB && x.Cmp(y) == 0
}
