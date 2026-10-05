//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_config_write_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The three claims about the config WRITE half that the shared contract cannot
// make, because each of them is about something only THIS leg has: an operation
// that redacts its own answer, an operation that bounds a value the role does
// not, and a PUT whose second call must not be short-circuited.
//
// They are here rather than in the unit tier because each needs a real server:
// the redaction predicate, the two edge bounds and the write-through are all the
// SERVER's, and a stub transport would let this file assert whatever it liked
// about them.

// TestServedConfigWriteAnswersTheReadThatFollowsIt is the redaction doctrine,
// pinned on both sides of the one key it changes anything for.
//
// The operation PERMITS a credential-bearing write and WITHHOLDS the value from
// its own answer — one projection serves the write and the read, so the body of
// a PUT is byte-identical to the body of the GET after it. That leaves a
// successful write whose `value` member is absent, and the role's result has no
// spelling for "withheld": reporting "" would tell a caller the plane holds
// nothing under that key, which is the write-side version of the confusion
// RedactedSettingError exists to prevent on the read side.
//
// So the client answers with the value it SENT, and this asserts why that is
// honest rather than convenient: the value really is stored, read RAW from the
// reference store past the wire that hid it.
//
// WHAT THIS CASE CANNOT SAY is that the ordinary key beside it is TRANSCRIBED
// off the answer rather than echoed from the request. Against a correct server
// the two are byte-identical — the operation promises the stored value equals
// the value sent for every key it accepts — so the difference is pinned where it
// is observable, against a transport that answers something else:
// TestSetSettingTranscribesTheAnswerRatherThanEchoingTheRequest. The ordinary
// key runs here anyway, because a client that transcribed the WRONG member would
// fail it.
func TestServedConfigWriteAnswersTheReadThatFollowsIt(t *testing.T) {
	e := newServedEnv(t, "hcfgw1")
	ctx := t.Context()
	settings := servedSettings(t, e)

	const secretKey = "notion.token"
	const secret = "shhh-real-secret"
	res, err := settings.SetSetting(ctx, issueops.SetSettingRequest{Key: secretKey, Value: secret})
	if err != nil {
		t.Fatalf("SetSetting on a credential-bearing key = %v; the write is permitted and only the ANSWER withholds", err)
	}
	if res.Key != secretKey {
		t.Errorf("SetSetting echoed key %q, want %q", res.Key, secretKey)
	}
	if res.Value != secret {
		t.Errorf("SetSetting reported value %q, want %q: the answer withheld it, and \"\" would say the plane stores nothing there",
			res.Value, secret)
	}

	// Read RAW, past the role and past the wire: the point is that the value the
	// response would not repeat IS the value the workspace now holds.
	stored, err := e.reference.GetConfig(ctx, secretKey)
	if err != nil {
		t.Fatalf("read the stored credential back from the reference store: %v", err)
	}
	if stored != secret {
		t.Errorf("the workspace stores %q under %q, want %q", stored, secretKey, secret)
	}

	// And the READ that follows still withholds, which is the other half of "the
	// PUT body is the GET body": a write does not unlock the key it just wrote.
	if _, err := settings.GetSetting(ctx, issueops.GetSettingRequest{Key: secretKey}); !errors.Is(err, ErrSettingRedacted) {
		t.Errorf("GetSetting after the write = %v, want ErrSettingRedacted; the write does not unlock the read", err)
	}

	// The ordinary key is TRANSCRIBED, not echoed. The server answers this one
	// with the stored value on the wire, so a client that reported the request
	// unconditionally would be indistinguishable here — until a server stored
	// something else.
	const plainKey = "custom.hcfgw1-plain"
	const plain = "  spaces and , commas  "
	res, err = settings.SetSetting(ctx, issueops.SetSettingRequest{Key: plainKey, Value: plain})
	if err != nil {
		t.Fatalf("SetSetting on an ordinary key: %v", err)
	}
	if res.Value != plain {
		t.Errorf("SetSetting reported %q, want the verbatim %q", res.Value, plain)
	}
	got, err := settings.GetSetting(ctx, issueops.GetSettingRequest{Key: plainKey})
	if err != nil || got.Value != plain {
		t.Errorf("GetSetting after the write = (%q, %v), want (%q, nil)", got.Value, err, plain)
	}
}

// TestServedConfigSetTwiceReachesTheRoleBothTimes pins that this client adds no
// idempotence of its own.
//
// The role's contract is explicit that a write of the value already there still
// performs the write and its projection — a no-op detection would make repairing
// a normalized table that had drifted from its row depend on the row having
// changed — and the wire carries no `changed` member to tempt one. A client that
// compared first would be the only leg on which that repair silently stopped
// happening, and nothing else in this tree would notice.
//
// It is measured through the PROJECTION rather than through the row, because the
// row is what a short-circuit would leave looking correct: the table is
// desynchronized out of band, the same value is written again, and the table has
// to come back.
func TestServedConfigSetTwiceReachesTheRoleBothTimes(t *testing.T) {
	e := newServedEnv(t, "hcfgw2")
	ctx := t.Context()
	settings := servedSettings(t, e)

	const value = "research,ops"
	first, err := settings.SetSetting(ctx, issueops.SetSettingRequest{Key: issueops.SettingKeyTypesCustom, Value: value})
	if err != nil {
		t.Fatalf("the first SetSetting: %v", err)
	}
	if got := servedCustomTypeRows(t, ctx, e); got != 2 {
		t.Fatalf("custom_types holds %d rows after the first write, want 2", got)
	}

	// Desynchronize the projection PAST the role, which is the state the role's
	// contract says a repeated write exists to repair.
	if err := e.exec(ctx, []conformance.SQLStatement{{Query: "DELETE FROM custom_types"}}); err != nil {
		t.Fatalf("desynchronize the projection: %v", err)
	}
	if got := servedCustomTypeRows(t, ctx, e); got != 0 {
		t.Fatalf("custom_types holds %d rows after the delete, want 0; the case proves nothing", got)
	}

	second, err := settings.SetSetting(ctx, issueops.SetSettingRequest{Key: issueops.SettingKeyTypesCustom, Value: value})
	if err != nil {
		t.Fatalf("the second SetSetting: %v", err)
	}
	if got := servedCustomTypeRows(t, ctx, e); got != 2 {
		t.Errorf("custom_types holds %d rows after re-writing the same value, want 2: the second write did not reach the role, "+
			"so a table that had drifted from its row can never be repaired through this client", got)
	}
	// And the two answers are the same answer. There is no `already_set` on this
	// surface and this client must not invent one.
	if first != second {
		t.Errorf("the two writes answered %+v and %+v; a PUT's second answer is its first", first, second)
	}
}

// TestServedConfigWriteBoundsAreTheServersAndNotThisClients is L-config-bounds,
// measured against the reference store through its OWN role so "the two legs
// differ" is observed rather than asserted.
//
// Both bounds are the OPERATION's: internal/httpapi checks them before the role
// is called, and workapi.ValidateSettingWrite — the rule every leg shares —
// applies neither. So the same request is a typed, member-named refusal here and
// a raw column overflow there, and this client restates nothing, for the reason
// the ledger row gives.
//
// THE CEILING ITSELF IS ASSERTED FIRST. Without it the case would pass against a
// client that refused every value: what makes the refusal a BOUND rather than a
// blanket is that exactly types.MaxTextBytes bytes is stored.
func TestServedConfigWriteBoundsAreTheServersAndNotThisClients(t *testing.T) {
	e := newServedEnv(t, "hcfgw3")
	ctx := t.Context()
	settings := servedSettings(t, e)
	reference, err := e.reference.WorkspaceConfig()
	if err != nil {
		t.Fatalf("the reference store's WorkspaceConfig(): %v", err)
	}

	atCeiling := strings.Repeat("y", types.MaxTextBytes)
	if _, err := settings.SetSetting(ctx, issueops.SetSettingRequest{Key: "custom.hcfgw3-at", Value: atCeiling}); err != nil {
		t.Fatalf("a value of exactly %d bytes was refused: %v", types.MaxTextBytes, err)
	}

	past := strings.Repeat("y", types.MaxTextBytes+1)
	httpErr := setSettingErr(ctx, settings, "custom.hcfgw3-past", past)
	if !errors.Is(httpErr, issueops.ErrValidation) {
		t.Errorf("a value one byte past the column = %v, want ErrValidation carried back from the operation's 400", httpErr)
	}
	if refErr := setSettingErr(ctx, reference, "custom.hcfgw3-past-local", past); errors.Is(refErr, issueops.ErrValidation) {
		t.Errorf("the reference store answered ErrValidation for the same value (%v); if both legs now classify it, "+
			"L-config-bounds has retired and this pin should go with it", refErr)
	} else if refErr == nil {
		t.Error("the reference store STORED a value past the column; the bound this row is about does not exist")
	}

	// The KEY bound splits the same way — and only on the WRITE. The remove
	// checks that the key names something and nothing more, on both legs.
	longKey := "custom.hcfgw3-" + strings.Repeat("k", types.MaxFieldLen)
	if err := setSettingErr(ctx, settings, longKey, "v"); !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("a key past the column on the write = %v, want ErrValidation", err)
	}
	if err := setSettingErr(ctx, reference, longKey+"2", "v"); errors.Is(err, issueops.ErrValidation) {
		t.Errorf("the reference store answered ErrValidation for the same key (%v); the key half of L-config-bounds has retired", err)
	}
	if _, err := settings.UnsetSetting(ctx, issueops.UnsetSettingRequest{Key: longKey}); err != nil {
		t.Errorf("removing an over-long key = %v, want success: the remove carries no length bound on either leg", err)
	}
}

// servedSettings is the SUBJECT's settings role, which every case here drives.
func servedSettings(t *testing.T, e *servedEnv) issueops.WorkspaceConfig {
	t.Helper()
	settings, err := e.subject.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	return settings
}

// setSettingErr is the error half of a write, for the arms that are about the
// refusal rather than the result.
func setSettingErr(ctx context.Context, settings issueops.WorkspaceConfig, key, value string) error {
	_, err := settings.SetSetting(ctx, issueops.SetSettingRequest{Key: key, Value: value})
	return err
}

// servedCustomTypeRows counts the projected table the write re-synchronizes.
func servedCustomTypeRows(t *testing.T, ctx context.Context, e *servedEnv) int {
	t.Helper()
	var rows int
	if err := e.queryScalar(ctx, "SELECT COUNT(*) FROM custom_types", nil, &rows); err != nil {
		t.Fatalf("count custom_types: %v", err)
	}
	return rows
}
