// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wave2c_config_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The unit half of client wave 2c's config writes: what the client SENDS, and
// which refusals it raises without dialing at all.
//
// The served tier runs the whole twenty-one-case contract and three pins of its
// own, so what is left here is the two facts a correct server hides. A client
// that skipped its own validation would still pass every contract case — the
// server refuses the same three requests at the edge — while spending a round
// trip to be told what the role's own contract already calls invalid, and
// binding the classification to a problem body a future server release might
// spell differently. And a client that sent the wrong METHOD or the wrong PATH
// would fail loudly against a server but says nothing about which of the two was
// wrong; a recorded request does.

// TestSettingWriteValidationMatchesTheSharedValidator is the drift pin for the
// one validator this package had to copy.
//
// depguard denies internal/workapi to this package — a client that could build a
// filter is a client whose narrowing no server-side gate can observe — but not
// to a test file, which is the exemption releaser.go and counter.go already use
// for the same reason. Driving both bodies over one table is what makes them
// agree case by case rather than in prose.
//
// THE VALUE IS PART OF THE TABLE, not only the key, because one of the three
// rules is about a value: `status.custom` is projected into a lookup table reads
// consult first, so a value that cannot be projected must not become a row.
func TestSettingWriteValidationMatchesTheSharedValidator(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"an ordinary key", "routing.default", "core"},
		{"an empty key", "", "x"},
		{"a blank key", "   ", "x"},
		{"the protected key", issueops.SettingKeyIssuePrefix, "bd"},
		{"the protected key, dashed", settingKeyIssuePrefixDashed, "bd"},
		{"a parseable custom status", issueops.SettingKeyStatusCustom, "awaiting_review:active"},
		{"an unparseable custom status", issueops.SettingKeyStatusCustom, "open"},
		{"an empty custom status", issueops.SettingKeyStatusCustom, ""},
		{"a custom type list", issueops.SettingKeyTypesCustom, "not:a:status"},
		{"a key that only LOOKS protected", "issue_prefix_backup", "bd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mine := validateSettingWrite(tc.key, tc.value)
			_, theirs := workapi.ValidateSettingWrite(tc.key, tc.value)
			if (mine == nil) != (theirs == nil) {
				t.Fatalf("validateSettingWrite(%q, %q) = %v but workapi.ValidateSettingWrite = %v: "+
					"this leg refuses a different set from the one every other leg refuses", tc.key, tc.value, mine, theirs)
			}
			if mine != nil && !errors.Is(mine, issueops.ErrValidation) {
				t.Errorf("validateSettingWrite(%q, %q) = %v, which does not match ErrValidation", tc.key, tc.value, mine)
			}
		})
	}
}

// TestSettingKeyValidationMatchesTheSharedValidator is the same pin for the
// narrower rule the READ and the REMOVE share.
//
// It is a separate case because the two validators refuse different sets on
// purpose: the protected key is refused on a write and NOT on a remove, which is
// shipped behavior on every implementation (bd-yby99.34) rather than something
// this client decides. A single table would have hidden that.
func TestSettingKeyValidationMatchesTheSharedValidator(t *testing.T) {
	for _, key := range []string{"routing.default", "", "   ", issueops.SettingKeyIssuePrefix, settingKeyIssuePrefixDashed} {
		mine := validateSettingKey(key)
		_, theirs := workapi.ValidateSettingKey(key)
		if (mine == nil) != (theirs == nil) {
			t.Errorf("validateSettingKey(%q) = %v but workapi.ValidateSettingKey = %v", key, mine, theirs)
		}
		if mine != nil && !errors.Is(mine, issueops.ErrValidation) {
			t.Errorf("validateSettingKey(%q) = %v, which does not match ErrValidation", key, mine)
		}
	}
}

// TestConfigWritesRefuseBeforeTheDial is the ordering half of the rule above.
//
// Agreeing with the shared validator is worth nothing if the refusal happens
// after the request has already been sent: the point of restating a role rule is
// that an invalid request does not consume a write slot to be told so. This
// drives every refusing shape against a recording transport and asserts nothing
// left the process.
func TestConfigWritesRefuseBeforeTheDial(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(issueops.WorkspaceConfig) error
	}{
		{"set with an empty key", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: "  ", Value: "x"})
			return err
		}},
		{"set of the protected key", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: issueops.SettingKeyIssuePrefix, Value: "bd"})
			return err
		}},
		{"set of an unparseable custom status", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: issueops.SettingKeyStatusCustom, Value: "open"})
			return err
		}},
		{"unset with an empty key", func(c issueops.WorkspaceConfig) error {
			_, err := c.UnsetSetting(context.Background(), issueops.UnsetSettingRequest{Key: ""})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, w := recordingStore(t)
			settings, err := store.WorkspaceConfig()
			if err != nil {
				t.Fatalf("WorkspaceConfig(): %v", err)
			}
			if err := tc.call(settings); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("refusal = %v, want ErrValidation", err)
			}
			if len(w.dispatched) != 0 {
				t.Errorf("the refused request still dialed %v; a request the role calls invalid must not reach the server", w.dispatched)
			}
		})
	}
}

// TestConfigWritesDialTheKeyInThePath pins the two requests member for member:
// the verb, the path the key becomes, and the body that carries the value alone.
//
// The KEY IS ESCAPED into one segment, which is why the case uses one that would
// otherwise change the request's shape. A slash would push the segment onto a
// path the server routes elsewhere, and a colon would make the custom-method
// split ambiguous — both are escapeSegment's job, and both are invisible against
// a server that simply 404s.
func TestConfigWritesDialTheKeyInThePath(t *testing.T) {
	store, w := recordingStore(t)
	settings, err := store.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	const key = "routing/mode:beta"

	if _, err := settings.SetSetting(context.Background(), issueops.SetSettingRequest{Key: key, Value: "core"}); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	set := w.requests[len(w.requests)-1]
	if set.Op != wire.OpSetSetting || set.Method != http.MethodPut {
		t.Errorf("the write dialed %s %s, want %s PUT", set.Op, set.Method, wire.OpSetSetting)
	}
	if want := "/v0/beads/config/routing%2Fmode%3Abeta"; set.Path != want {
		t.Errorf("the write dialed %q, want %q", set.Path, want)
	}
	if len(set.Query) != 0 {
		t.Errorf("the write carried a query (%v); this operation takes none and refuses one", set.Query)
	}
	body, ok := set.Body.(apigen.SetSettingRequest)
	if !ok {
		t.Fatalf("the write body is %T, want the generated SetSettingRequest", set.Body)
	}
	if body.Value != "core" {
		t.Errorf("the write body carried %q, want %q", body.Value, "core")
	}

	if _, err := settings.UnsetSetting(context.Background(), issueops.UnsetSettingRequest{Key: key}); err != nil {
		t.Fatalf("UnsetSetting: %v", err)
	}
	unset := w.requests[len(w.requests)-1]
	if unset.Op != wire.OpUnsetSetting || unset.Method != http.MethodDelete {
		t.Errorf("the remove dialed %s %s, want %s DELETE", unset.Op, unset.Method, wire.OpUnsetSetting)
	}
	if unset.Path != set.Path {
		t.Errorf("the remove dialed %q and the write dialed %q; both name one resource", unset.Path, set.Path)
	}
	if unset.Body != nil {
		t.Errorf("the remove carried a body (%v); the key is the whole request", unset.Body)
	}
}

// TestSetSettingTranscribesTheAnswerRatherThanEchoingTheRequest is the arm no
// server can prove.
//
// The operation promises that the stored value equals the value sent for every
// key it accepts, so against a correct server a client that transcribed the
// answer and one that echoed its own request are byte-identical — which is
// exactly why the difference has to be pinned against a transport that answers
// something else. A client that echoed would report a value the workspace does
// not hold the day a server normalized one, and would do it silently.
//
// The REDACTED arm is the other half, and it is the one place the echo IS the
// right answer: the projection withholds the value it just accepted, so there is
// nothing to transcribe and the request is the only honest source. Both arms in
// one case so neither can be satisfied by a body that always does one of them.
func TestSetSettingTranscribesTheAnswerRatherThanEchoingTheRequest(t *testing.T) {
	const sent = "what the caller sent"

	stored := "what the server stored"
	plain := &settingAnswerWire{answer: apigen.Setting{Key: "routing.default", Value: &stored}}
	res, err := settingsOver(t, plain).SetSetting(context.Background(),
		issueops.SetSettingRequest{Key: "routing.default", Value: sent})
	if err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if res.Value != stored {
		t.Errorf("SetSetting reported %q, want the answer's %q: the result is what the workspace holds, "+
			"and echoing the request would report a value it does not", res.Value, stored)
	}
	if res.Key != "routing.default" {
		t.Errorf("SetSetting reported key %q, want the answer's", res.Key)
	}

	withheld := &settingAnswerWire{answer: apigen.Setting{Key: "notion.token", Redacted: true}}
	res, err = settingsOver(t, withheld).SetSetting(context.Background(),
		issueops.SetSettingRequest{Key: "notion.token", Value: sent})
	if err != nil {
		t.Fatalf("SetSetting on a credential-bearing key: %v", err)
	}
	if res.Value != sent {
		t.Errorf("SetSetting on a withheld answer reported %q, want the request's %q: \"\" would say the plane "+
			"stores nothing under a key it certainly stores something under", res.Value, sent)
	}
}

// settingAnswerWire is a transport that answers one prepared Setting, which is
// how the case above drives a server that says something the request did not.
type settingAnswerWire struct {
	recordingWire

	answer apigen.Setting
}

func (w *settingAnswerWire) Do(ctx context.Context, req wire.Request, out any) error {
	if err := w.recordingWire.Do(ctx, req, out); err != nil {
		return err
	}
	if body, ok := out.(*apigen.Setting); ok {
		*body = w.answer
	}
	return nil
}

func settingsOver(t *testing.T, w WireClient) issueops.WorkspaceConfig {
	t.Helper()
	settings, err := New(testTarget(t), w, &apigen.ContextResponse{}).WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	return settings
}

// TestConfigWriteBoundsAreNotRestatedClientSide is the observable half of
// L-config-bounds, and it exists because the served pin cannot make it.
//
// Over http an oversized value and an oversized key both come back as
// ErrValidation whether this client refused them or the operation did, so the
// served case that measures the divergence against a local leg passes either
// way — a client that had quietly grown its own copy of the two bounds would
// look identical there. What separates them is whether the request LEFT: the
// ledger row says these are the SERVER's rules, one release away from moving, so
// a client that refused them locally would be a second copy free to drift, and
// would refuse tomorrow's server a request it would have taken.
//
// The three rules this client DOES restate are refused before the dial by the
// case above, so the two together say exactly where the line is.
func TestConfigWriteBoundsAreNotRestatedClientSide(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"a value past the column", "custom.big", strings.Repeat("v", types.MaxTextBytes+1)},
		{"a key past the column", "custom." + strings.Repeat("k", types.MaxFieldLen), "v"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, w := recordingStore(t)
			settings, err := store.WorkspaceConfig()
			if err != nil {
				t.Fatalf("WorkspaceConfig(): %v", err)
			}
			if _, err := settings.SetSetting(context.Background(), issueops.SetSettingRequest{Key: tc.key, Value: tc.value}); err != nil {
				t.Fatalf("SetSetting = %v, want the request to travel and the SERVER to refuse it", err)
			}
			if len(w.dispatched) != 1 || w.dispatched[0] != wire.OpSetSetting {
				t.Errorf("the write dialed %v, want exactly [%s]: the length rules are the operation's, and a "+
					"client-side copy of them is one release away from refusing a request a newer server would take",
					w.dispatched, wire.OpSetSetting)
			}
		})
	}
}
