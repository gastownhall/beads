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

// TestServedConfigWriteAnswersTheReadThatFollowsIt pins GH#7559 end to end
// against a real served role, beside the unrelated transcription property the
// ordinary key below it was always here to prove.
//
// BEFORE GH#7559 this case pinned the redaction doctrine: a credential-bearing
// write was PERMITTED and only the ANSWER withheld the value. That doctrine
// was wrong for this door — the credential landed in the Dolt config table,
// a table `bd dolt push`/pull replicates to every remote — so the fix refuses
// the write instead of merely hiding its result. There is nothing left for
// this case to prove about REDACTION on a key workapi.ValidateSettingWrite now
// refuses outright; the httpapi package's fake-role tests
// (TestSetSettingAnswersWhatTheReadWouldAnswer, TestSetSettingWithholdsACredentialItJustAccepted)
// keep pinning what the redaction PROJECTION does if a role ever did accept
// such a write.
func TestServedConfigWriteAnswersTheReadThatFollowsIt(t *testing.T) {
	e := newServedEnv(t, "hcfgw1")
	ctx := t.Context()
	settings := servedSettings(t, e)

	const secretKey = "notion.token"
	const secret = "shhh-real-secret"
	_, err := settings.SetSetting(ctx, issueops.SetSettingRequest{Key: secretKey, Value: secret})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("SetSetting on a credential-bearing key = %v, want ErrValidation: "+
			"this plane is the Dolt config table, synced to every remote on push/pull", err)
	}

	// Read RAW, past the role and past the wire: nothing was written.
	stored, err := e.reference.GetConfig(ctx, secretKey)
	if err != nil {
		t.Fatalf("read the reference store for the key that must not have been written: %v", err)
	}
	if stored != "" {
		t.Errorf("the workspace stores %q under %q, want nothing: the refused write must not have reached the store", stored, secretKey)
	}

	// The READ side is unchanged by this fix and keeps its own doctrine: a
	// GetSetting on a credential-bearing key withholds unconditionally, keyed
	// on the KEY pattern rather than on whether anything is actually stored
	// there — confirmed empirically (this is not something the write-side fix
	// touches, so the refused write above leaves it exactly as it always was).
	if _, err := settings.GetSetting(ctx, issueops.GetSettingRequest{Key: secretKey}); !errors.Is(err, ErrSettingRedacted) {
		t.Errorf("GetSetting on a credential-bearing key = %v, want ErrSettingRedacted regardless of whether a write ever reached the store", err)
	}

	// The ordinary key is TRANSCRIBED, not echoed. The server answers this one
	// with the stored value on the wire, so a client that reported the request
	// unconditionally would be indistinguishable here — until a server stored
	// something else.
	const plainKey = "custom.hcfgw1-plain"
	const plain = "  spaces and , commas  "
	res, err := settings.SetSetting(ctx, issueops.SetSettingRequest{Key: plainKey, Value: plain})
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
